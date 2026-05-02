package wanix

import (
	"context"
	"strings"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
)

// TestSpawnedTaskInheritsParentFDs covers the asymmetry between markup-spawned
// and code-spawned tasks. The JS <wanix-task> element binds fd/0,1,2 onto the
// task's namespace after allocation (elements/task.js:87-94). Tasks allocated
// from Go via #task/new/auto used to skip that step, so any caller (rc's
// shell/exec_wanix.go:37 is the real one) opening #task/<rid>/fd/1 hit
// ErrNotExist.
//
// The fix in TaskFS.Alloc inherits the parent's fd/0,1,2 onto the child by
// binding parent's #task/<parent.id>/fd/<n> onto child's
// #task/<child.id>/fd/<n>. This matches unix fork/execve semantics. Children
// can rebind before writing "start" to ctl if they want to redirect.
//
// This test sets up a parent that has fd/0,1,2 (the way JS would), allocates
// a child via #task/new/auto, and asserts the child's fd paths resolve to the
// same data the parent bound. Failure modes: ErrNotExist (no inheritance) or
// reading wrong content (bound the wrong source).
func TestSpawnedTaskInheritsParentFDs(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}

	parentMarker := []byte("parent-fd-payload\n")
	for _, fd := range []string{"0", "1", "2"} {
		src := fskit.MapFS{
			"data": fskit.RawNode(parentMarker, 0644),
		}
		dst := "#task/" + root.ID() + "/fd/" + fd
		if err := root.Namespace().Bind(src, "data", dst); err != nil {
			t.Fatalf("bind parent fd/%s: %v", fd, err)
		}
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
	if rid == root.ID() {
		t.Fatalf("child rid %q should differ from root %q", rid, root.ID())
	}

	child, err := root.Lookup(rid)
	if err != nil {
		t.Fatalf("lookup child: %v", err)
	}
	for _, fd := range []string{"0", "1", "2"} {
		path := "#task/" + rid + "/fd/" + fd
		got, err := fs.ReadFile(child.Namespace(), path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if string(got) != string(parentMarker) {
			t.Errorf("read %s: got %q, want %q", path, got, parentMarker)
		}
	}
}
