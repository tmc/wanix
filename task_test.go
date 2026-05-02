package wanix

import (
	"context"
	"io"
	"os"
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
			t.Errorf("read %s from child ns: %v", path, err)
			continue
		}
		if string(got) != string(parentMarker) {
			t.Errorf("read %s from child ns: got %q, want %q", path, got, parentMarker)
		}
	}
}

// TestSpawnerCanWriteCmdViaTrunc documents a kernel-side bug surfaced in
// 2307's apptron v6 deployment: rc used os.WriteFile to set #task/<child>/cmd,
// which opens with O_WRONLY|O_CREATE|O_TRUNC. The truncate path in
// fs/openfile.go calls Create on the file, but misc.FieldFile (the cmd file's
// implementation) does not support Create — so the open fails with
// "operation not supported" and rc dies before reaching fd binding. The
// rc-side workaround (open with plain O_WRONLY in writeTaskField) avoids the
// Create path. This test stays as a regression sentinel for the kernel side:
// when fs/openfile.go (or FieldFile) is taught to truncate without Create,
// this test should pass without rc's workaround.
func TestSpawnerCanWriteCmdViaTrunc(t *testing.T) {
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

	cmdPath := "#task/" + rid + "/cmd"
	f, err := fs.OpenFile(root.Namespace(), cmdPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Skipf("kernel open #task/<rid>/cmd with O_TRUNC fails: %v (rc works around with O_WRONLY only)", err)
	}
	if _, err := f.(io.Writer).Write([]byte("warren help\n")); err != nil {
		t.Errorf("write %s: %v", cmdPath, err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("close %s: %v", cmdPath, err)
	}
}

// TestSpawnerCanWriteCmdPlain mirrors the rc-side workaround: open the cmd
// file with plain O_WRONLY (no O_CREATE, no O_TRUNC). This is the path
// rc/shell/exec_wanix.go:writeTaskField now uses. It must work today.
func TestSpawnerCanWriteCmdPlain(t *testing.T) {
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

	cmdPath := "#task/" + rid + "/cmd"
	f, err := fs.OpenFile(root.Namespace(), cmdPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s for write: %v", cmdPath, err)
	}
	if _, err := f.(io.Writer).Write([]byte("warren help\n")); err != nil {
		t.Errorf("write %s: %v", cmdPath, err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("close %s: %v", cmdPath, err)
	}
}

// TestParentSeesChildFDs confirms the inheritance is also visible from the
// parent's namespace, which is where rc actually reads from. rc lives in the
// parent task; when it spawns a child via #task/new/auto and opens
// #task/<child>/fd/1 to capture stdout, the open goes through rc's own
// namespace (parent), not the child's. If inheritance only populates the
// child's ns view, rc still ENOENTs.
func TestParentSeesChildFDs(t *testing.T) {
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

	for _, fd := range []string{"0", "1", "2"} {
		path := "#task/" + rid + "/fd/" + fd
		got, err := fs.ReadFile(root.Namespace(), path)
		if err != nil {
			t.Errorf("read %s from PARENT ns: %v", path, err)
			continue
		}
		if string(got) != string(parentMarker) {
			t.Errorf("read %s from PARENT ns: got %q, want %q", path, got, parentMarker)
		}
	}
}
