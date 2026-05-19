package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
	"tractor.dev/wanix/checkpoint"
)

// Main runs the rc shell and returns its process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("rc", flag.ContinueOnError)
	flags.SetOutput(stderr)
	command := flags.String("c", "", "shell command to run")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	wd, err := os.Getwd()
	if err != nil {
		fatalf(stderr, "rc: %v\n", err)
		return 1
	}
	env := expand.ListEnviron(os.Environ()...)
	rcState := newCheckpointState(wd, exportedEnvPairs(env))
	if *command == "" && flags.NArg() == 0 {
		if err := checkpoint.Register(checkpoint.Handler{
			Load: rcState.load,
			Save: rcState.save,
		}); err != nil && !errors.Is(err, checkpoint.ErrUnsupported) {
			fatalf(stderr, "rc: %v\n", err)
			return 1
		}
		wd = rcState.dir()
		env = expand.ListEnviron(rcState.env()...)
	}

	runner, err := interp.New(
		interp.Dir(wd),
		interp.Env(env),
		interp.StdIO(stdin, stdout, stderr),
		interp.Interactive(*command == "" && flags.NArg() == 0),
		interp.CallHandler(rcCallHandler(rcState)),
		interp.ExecHandlers(urootCoreutilsMiddleware()),
		interp.ExecHandlers(wanixExecMiddleware()),
	)
	if err != nil {
		fatalf(stderr, "rc: %v\n", err)
		return 1
	}

	parser := syntax.NewParser(syntax.KeepComments(true))
	ctx := context.Background()

	switch {
	case *command != "":
		if err := runSource(ctx, runner, parser, "-c", strings.NewReader(*command)); err != nil {
			return exitCodeForErr(stderr, err)
		}
	case flags.NArg() > 0:
		file := flags.Arg(0)
		f, err := os.Open(file)
		if err != nil {
			fatalf(stderr, "rc: %v\n", err)
			return 1
		}
		defer f.Close()
		if err := runSource(ctx, runner, parser, file, f); err != nil {
			return exitCodeForErr(stderr, err)
		}
	default:
		if err := runREPL(ctx, runner, parser, stdin, stderr, rcState); err != nil {
			return exitCodeForErr(stderr, err)
		}
	}
	return 0
}

func runREPL(ctx context.Context, r *interp.Runner, parser *syntax.Parser, stdin io.Reader, stderr io.Writer, state *checkpointState) error {
	scanner := bufio.NewScanner(stdin)
	for {
		state.setPrompt(r.Dir)
		fmt.Fprint(stderr, "rc% ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil
		}
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		state.addHistory(line)
		state.setRunning(r.Dir)
		if err := runSource(ctx, r, parser, "<stdin>", strings.NewReader(line+"\n")); err != nil {
			var status interp.ExitStatus
			if errors.As(err, &status) {
				state.setStatus(r.Dir, int(status))
				fmt.Fprintf(stderr, "exit status %d\n", status)
				continue
			}
			return err
		}
		state.setStatus(r.Dir, 0)
		if r.Exited() {
			return nil
		}
	}
}

func runSource(ctx context.Context, r *interp.Runner, parser *syntax.Parser, name string, src io.Reader) error {
	prog, err := parser.Parse(src, name)
	if err != nil {
		return err
	}
	return r.Run(ctx, prog)
}

func wanixExecMiddleware() func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			if len(args) == 0 {
				return next(ctx, args)
			}
			path, err := resolveExecPath(hc, args[0])
			if err != nil {
				fmt.Fprintf(hc.Stderr, "rc: %s: %v\n", args[0], err)
				return interp.ExitStatus(127)
			}
			warnIfNotExecutable(hc, path)

			code, err := runExternalCommand(ctx, hc, path, args[1:])
			if err != nil {
				return err
			}
			if code == 0 {
				return nil
			}
			return interp.ExitStatus(code)
		}
	}
}

func resolveExecPath(hc interp.HandlerContext, cmd string) (string, error) {
	if filepath.IsAbs(cmd) || strings.HasPrefix(cmd, "./") || strings.HasPrefix(cmd, "../") {
		if _, err := os.Stat(cmd); err != nil {
			return "", err
		}
		return cmd, nil
	}

	pathVar := hc.Env.Get("PATH")
	pathEntries := []string{""}
	if pathVar.IsSet() && pathVar.Str != "" {
		pathEntries = strings.Split(pathVar.Str, ":")
	}

	candidates := make([]string, 0, len(pathEntries)+1)
	if strings.Contains(cmd, "/") {
		// Plan 9-like behavior: allow subpaths in PATH lookups.
		for _, base := range pathEntries {
			if base == "" {
				candidates = append(candidates, cmd)
				continue
			}
			candidates = append(candidates, filepath.Join(base, cmd))
		}
		candidates = append(candidates, cmd)
	} else {
		for _, base := range pathEntries {
			if base == "" {
				candidates = append(candidates, cmd)
				continue
			}
			candidates = append(candidates, filepath.Join(base, cmd))
		}
	}

	for _, candidate := range candidates {
		full := candidate
		if !filepath.IsAbs(full) {
			full = filepath.Join(hc.Dir, full)
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		return candidate, nil
	}
	return "", fs.ErrNotExist
}

func warnIfNotExecutable(hc interp.HandlerContext, path string) {
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(hc.Dir, path)
	}
	info, err := os.Stat(full)
	if err != nil {
		return
	}
	if info.Mode()&0o111 == 0 {
		fmt.Fprintf(hc.Stderr, "rc: warning: %s is not marked executable\n", path)
	}
}

func exportedEnvPairs(env expand.Environ) []string {
	pairs := make([]string, 0, 64)
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.IsSet() && vr.Kind == expand.String {
			pairs = append(pairs, name+"="+vr.Str)
		}
		return true
	})
	return pairs
}

func printHelp(hc interp.HandlerContext) {
	builtins := []string{"help", "history", "cd", "pwd", "echo", "exit", "export", "unset", "type", ":", "true", "false"}
	embedded := bundledCommandNames()

	fmt.Fprintln(hc.Stdout, "rc help")
	fmt.Fprintln(hc.Stdout, "")
	fmt.Fprintln(hc.Stdout, "Usage:")
	fmt.Fprintln(hc.Stdout, "  help")
	fmt.Fprintln(hc.Stdout, "  help <command-or-builtin>")
	fmt.Fprintln(hc.Stdout, "")
	fmt.Fprintf(hc.Stdout, "Builtins: %s\n", strings.Join(builtins, ", "))
	fmt.Fprintf(hc.Stdout, "Bundled commands: %s\n", strings.Join(embedded, ", "))
	fmt.Fprintln(hc.Stdout, "External commands: resolved via PATH lookup")
}

func rcCallHandler(state *checkpointState) interp.CallHandlerFunc {
	return func(ctx context.Context, args []string) ([]string, error) {
		state.recordCall(args)
		if len(args) > 0 && args[0] == "help" {
			if len(args) > 2 {
				return nil, fmt.Errorf("help: usage: help [command-or-builtin]")
			}
			if len(args) == 2 {
				return []string{args[1], "--help"}, nil
			}
			printHelp(interp.HandlerCtx(ctx))
			return []string{":"}, nil
		}
		if len(args) > 0 && args[0] == "history" {
			if len(args) > 1 {
				return nil, fmt.Errorf("history: usage: history")
			}
			printHistory(interp.HandlerCtx(ctx), state.history())
			return []string{":"}, nil
		}
		return args, nil
	}
}

func printHistory(hc interp.HandlerContext, history []string) {
	for i, line := range history {
		fmt.Fprintf(hc.Stdout, "%d\t%s\n", i+1, line)
	}
}

const maxCheckpointHistory = 100

type checkpointState struct {
	mu            sync.Mutex
	atPrompt      bool
	checkpointDir string
	checkpointEnv []string
	historyLines  []string
}

type rcCheckpoint struct {
	Version int      `json:"version"`
	Dir     string   `json:"dir"`
	Env     []string `json:"env"`
	History []string `json:"history,omitempty"`
}

func newCheckpointState(checkpointDir string, checkpointEnv []string) *checkpointState {
	return &checkpointState{
		atPrompt:      true,
		checkpointDir: checkpointDir,
		checkpointEnv: append([]string(nil), checkpointEnv...),
	}
}

func (s *checkpointState) dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpointDir
}

func (s *checkpointState) env() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.checkpointEnv...)
}

func (s *checkpointState) history() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.historyLines...)
}

func (s *checkpointState) load(data []byte) error {
	var saved rcCheckpoint
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	if saved.Version != 1 {
		return fmt.Errorf("unsupported rc checkpoint version %d", saved.Version)
	}
	if saved.Dir == "" {
		return fmt.Errorf("missing rc checkpoint directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointDir = saved.Dir
	s.checkpointEnv = append(s.checkpointEnv[:0], saved.Env...)
	s.historyLines = append(s.historyLines[:0], saved.History...)
	s.trimHistoryLocked()
	s.atPrompt = true
	return nil
}

func (s *checkpointState) save() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.atPrompt {
		return nil, checkpoint.ErrUnsupported
	}
	return json.Marshal(rcCheckpoint{
		Version: 1,
		Dir:     s.checkpointDir,
		Env:     append([]string(nil), s.checkpointEnv...),
		History: append([]string(nil), s.historyLines...),
	})
}

func (s *checkpointState) setPrompt(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointDir = dir
	s.atPrompt = true
}

func (s *checkpointState) setRunning(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointDir = dir
	s.atPrompt = false
}

func (s *checkpointState) setStatus(dir string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointDir = dir
	s.atPrompt = true
}

func (s *checkpointState) addHistory(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyLines = append(s.historyLines, line)
	s.trimHistoryLocked()
}

func (s *checkpointState) trimHistoryLocked() {
	if len(s.historyLines) <= maxCheckpointHistory {
		return
	}
	copy(s.historyLines, s.historyLines[len(s.historyLines)-maxCheckpointHistory:])
	s.historyLines = s.historyLines[:maxCheckpointHistory]
}

func (s *checkpointState) recordCall(args []string) {
	if len(args) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch args[0] {
	case "export":
		for _, arg := range args[1:] {
			name, value, ok := strings.Cut(arg, "=")
			if !ok || name == "" {
				continue
			}
			s.setEnvLocked(name, value)
		}
	case "unset":
		for _, name := range args[1:] {
			s.unsetEnvLocked(name)
		}
	}
}

func (s *checkpointState) setEnvLocked(name, value string) {
	prefix := name + "="
	for i, pair := range s.checkpointEnv {
		if strings.HasPrefix(pair, prefix) {
			s.checkpointEnv[i] = prefix + value
			return
		}
	}
	s.checkpointEnv = append(s.checkpointEnv, prefix+value)
}

func (s *checkpointState) unsetEnvLocked(name string) {
	prefix := name + "="
	for i, pair := range s.checkpointEnv {
		if strings.HasPrefix(pair, prefix) {
			s.checkpointEnv = append(s.checkpointEnv[:i], s.checkpointEnv[i+1:]...)
			return
		}
	}
}

func exitCodeForErr(stderr io.Writer, err error) int {
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	fatalf(stderr, "rc: %v\n", err)
	return 1
}

func fatalf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
