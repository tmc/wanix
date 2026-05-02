package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// historyCap is the maximum number of recalled commands.
const historyCap = 256

// runREPL runs the interactive read-eval-print loop. If stdin is a terminal it
// uses golang.org/x/term to provide line editing (left/right, home/end,
// backspace) and arrow-key history. Otherwise it falls back to a plain
// scanner so that piped input still works.
func runREPL(ctx context.Context, r *interp.Runner, parser *syntax.Parser, stdin io.Reader, stderr io.Writer) error {
	in, ok := stdin.(*os.File)
	if ok && term.IsTerminal(int(in.Fd())) {
		if err := runEditedREPL(ctx, r, parser, in, stderr); err != nil {
			if errors.Is(err, errLineEditUnsupported) {
				return runScannerREPL(ctx, r, parser, stdin, stderr)
			}
			return err
		}
		return nil
	}
	return runScannerREPL(ctx, r, parser, stdin, stderr)
}

// errLineEditUnsupported is returned by the line-editing setup when the
// underlying terminal cannot be driven (e.g. raw mode unavailable on a host
// where it is required). Callers fall back to the scanner REPL.
var errLineEditUnsupported = errors.New("line editing not supported on this terminal")

func runEditedREPL(ctx context.Context, r *interp.Runner, parser *syntax.Parser, in *os.File, stderr io.Writer) error {
	restore, err := makeTerminalRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	if restore != nil {
		defer restore()
	}

	rw := &readWriter{r: in, w: stderr}
	t := term.NewTerminal(rw, "rc% ")
	t.History = newRingHistory(historyCap)

	for {
		line, err := t.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := runSource(ctx, r, parser, "<stdin>", strings.NewReader(line+"\n")); err != nil {
			var status interp.ExitStatus
			if errors.As(err, &status) {
				fmt.Fprintf(stderr, "exit status %d\n", status)
				continue
			}
			return err
		}
		if r.Exited() {
			return nil
		}
	}
}

func runScannerREPL(ctx context.Context, r *interp.Runner, parser *syntax.Parser, stdin io.Reader, stderr io.Writer) error {
	scanner := bufio.NewScanner(stdin)
	for {
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
		if err := runSource(ctx, r, parser, "<stdin>", strings.NewReader(line+"\n")); err != nil {
			var status interp.ExitStatus
			if errors.As(err, &status) {
				fmt.Fprintf(stderr, "exit status %d\n", status)
				continue
			}
			return err
		}
		if r.Exited() {
			return nil
		}
	}
}

// readWriter joins separate read and write halves into a single io.ReadWriter
// for term.Terminal, which insists on a unified channel.
type readWriter struct {
	r io.Reader
	w io.Writer
}

func (rw *readWriter) Read(p []byte) (int, error)  { return rw.r.Read(p) }
func (rw *readWriter) Write(p []byte) (int, error) { return rw.w.Write(p) }

// ringHistory is a fixed-capacity command history. It is the History plugged
// into term.Terminal, providing arrow-key recall without persistence.
type ringHistory struct {
	mu      sync.Mutex
	entries []string
	cap     int
}

func newRingHistory(cap int) *ringHistory {
	if cap <= 0 {
		cap = 1
	}
	return &ringHistory{cap: cap}
}

func (h *ringHistory) Add(entry string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.entries) == h.cap {
		copy(h.entries, h.entries[1:])
		h.entries[h.cap-1] = entry
		return
	}
	h.entries = append(h.entries, entry)
}

func (h *ringHistory) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// At returns the n-th most recent entry: At(0) is the newest.
func (h *ringHistory) At(n int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n < 0 || n >= len(h.entries) {
		return ""
	}
	return h.entries[len(h.entries)-1-n]
}
