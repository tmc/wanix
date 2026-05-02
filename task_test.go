package wanix

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tractor.dev/wanix/fs"
)

// TestSpawnedTaskHasFDPaths reproduces a server-side spawn failure observed in
// rc/shell/exec_wanix.go: rc allocates an external command via #task/new/auto,
// writes cmd/env/dir, and then opens #task/<rid>/fd/1 to read stdout. That
// open fails with ErrNotExist because nothing in the Go-side TaskFS path
// creates fd entries. The JS <wanix-task> element binds fd/0,1,2 explicitly
// after allocation (elements/task.js:87-94), so DOM-spawned tasks work; tasks
// allocated from server-side code do not.
//
// This test reproduces the failure mode independent of rc, the worker, and any
// browser plumbing. It should fail today and pass once #task/new/auto either
// pre-creates fd/0,1,2 or inherits them from the parent. Either fix is
// acceptable; this test only asserts that fd/1 resolves on a freshly allocated
// child task.
func TestSpawnedTaskHasFDPaths(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), TaskContextKey, root)

	ridFile, err := fs.OpenContext(ctx, root.Namespace(), "#task/new/auto")
	if err != nil {
		t.Fatalf("open #task/new/auto: %v", err)
	}
	var ridBuf [16]byte
	n, err := ridFile.Read(ridBuf[:])
	if err != nil {
		t.Fatalf("read rid: %v", err)
	}
	ridFile.Close()
	rid := strings.TrimSpace(string(ridBuf[:n]))
	if rid == "" {
		t.Fatal("empty rid")
	}

	for _, fd := range []string{"fd/0", "fd/1", "fd/2"} {
		path := "#task/" + rid + "/" + fd
		f, err := fs.OpenContext(ctx, root.Namespace(), path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				t.Errorf("open %s: not found (server-side spawn allocates no fd entries; rc/shell/exec_wanix.go:37 hits this)", path)
				continue
			}
			t.Errorf("open %s: %v", path, err)
			continue
		}
		f.Close()
	}
}
