//go:build !(js && wasm)

package shell

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestBackgroundDoesNotBlock verifies that `cmd &` runs concurrently with
// the next statement. mvdan/sh's interp dispatches Background statements in a
// goroutine, so our ExecHandler does not need to know about `&`.
func TestBackgroundDoesNotBlock(t *testing.T) {
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := Main([]string{"-c", "sleep 0.5 & echo done"}, strings.NewReader(""), &stdout, &stderr)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "done" {
		t.Fatalf("stdout = %q, want %q", got, "done")
	}
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("shell waited for backgrounded sleep: elapsed %v", elapsed)
	}
}

// TestWaitBlocksOnBackground verifies that the `wait` builtin still blocks
// until backgrounded commands finish.
func TestWaitBlocksOnBackground(t *testing.T) {
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := Main([]string{"-c", "sleep 0.3 & wait"}, strings.NewReader(""), &stdout, &stderr)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr.String())
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("wait did not block: elapsed %v", elapsed)
	}
}
