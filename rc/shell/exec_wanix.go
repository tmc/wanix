//go:build js && wasm

package shell

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// runExternalCommand spawns a wanix task and blocks until it exits.
//
// Backgrounding (`cmd &`) is handled at the mvdan/sh interp layer: it spawns
// a subshell goroutine that calls this handler, so blocking here is correct
// even for backgrounded commands. The `wait` builtin blocks on those
// goroutines, which means it waits for the underlying tasks too.
func runExternalCommand(ctx context.Context, hc interp.HandlerContext, path string, args []string) (int, error) {
	argv := append([]string{path}, args...)
	ridRaw, err := os.ReadFile("#task/new/auto")
	if err != nil {
		return 1, err
	}
	rid := strings.TrimSpace(string(ridRaw))
	base := filepath.Join("#task", rid)

	if err := writeTaskField(filepath.Join(base, "cmd"), []byte(joinTaskArgs(argv))); err != nil {
		return 1, err
	}
	if err := writeTaskField(filepath.Join(base, "env"), []byte(joinTaskEnv(hc))); err != nil {
		return 1, err
	}
	if err := writeTaskField(filepath.Join(base, "dir"), []byte(hc.Dir)); err != nil {
		return 1, err
	}

	stdoutFile, err := os.Open(filepath.Join(base, "fd/1"))
	if err != nil {
		return 1, err
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Open(filepath.Join(base, "fd/2"))
	if err != nil {
		return 1, err
	}
	defer stderrFile.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(hc.Stdout, stdoutFile)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(hc.Stderr, stderrFile)
	}()

	if shouldForwardStdin(hc.Stdin) {
		stdinFile, err := os.OpenFile(filepath.Join(base, "fd/0"), os.O_WRONLY, 0)
		if err != nil {
			return 1, err
		}
		go func() {
			_, _ = io.Copy(stdinFile, hc.Stdin)
			_ = stdinFile.Close()
		}()
	}

	if err := writeTaskField(filepath.Join(base, "ctl"), []byte("start")); err != nil {
		return 1, err
	}

	code, err := waitExitCode(ctx, filepath.Join(base, "exit"))
	if err != nil {
		return 1, err
	}

	_ = stdoutFile.Close()
	_ = stderrFile.Close()
	wg.Wait()
	return code, nil
}

// writeTaskField writes data to a wanix virtual field file (cmd, env, dir,
// ctl, etc.). It avoids os.WriteFile because that uses O_CREATE|O_TRUNC,
// which the wanix kernel currently rejects on virtual files (Create is not
// supported on misc.FieldFile, so the truncate-by-recreate path in
// fs/openfile.go fails with "operation not supported"). Plain O_WRONLY
// works because the field file's setter callback runs at Close and replaces
// the content with what was written.
func writeTaskField(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func shouldForwardStdin(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	return f.Fd() != os.Stdin.Fd()
}

func waitExitCode(ctx context.Context, path string) (int, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-ticker.C:
			out, err := os.ReadFile(path)
			if err != nil {
				return 1, err
			}
			out = []byte(strings.TrimSpace(string(out)))
			if len(out) == 0 {
				continue
			}
			code, err := strconv.Atoi(string(out))
			if err != nil {
				return 1, err
			}
			return code, nil
		}
	}
}

func joinTaskEnv(hc interp.HandlerContext) string {
	return strings.Join(append(exportedEnvPairs(hc.Env), ""), "\n")
}

func joinTaskArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteArg(arg)
	}
	return strings.Join(quoted, " ")
}

func quoteArg(in string) string {
	if in == "" {
		return "''"
	}
	if !strings.ContainsAny(in, " \t\n'\"\\") {
		return in
	}
	return "'" + strings.ReplaceAll(in, "'", `'"'"'`) + "'"
}
