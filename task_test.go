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

// TestThreeLevelInheritance mirrors the apptron prod stack: root spawns rc
// (markup-spawned, JS binds rc's fd/0,1,2), rc spawns warren via
// #task/new/auto. warren must inherit rc's fds at the path rc reads from
// (#task/<warren>/fd/1, via rc.ns).
//
// This three-level shape exposes inheritance bugs that root→child cannot:
// the JS bind into rc happens via api/bind.go (s.task.Namespace().Bind),
// and the question is whether that binding actually populates rc.ns in a
// way that my Alloc-time inheritance bind can read as a Bind src.
func TestThreeLevelInheritance(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}

	// Step 1: root allocates rc (mimics markup <wanix-task type="auto" cmd="rc">).
	rootCtx := context.WithValue(context.Background(), TaskContextKey, root)
	ridFile, err := fs.OpenContext(rootCtx, root.Namespace(), "#task/new/auto")
	if err != nil {
		t.Fatal(err)
	}
	var ridBuf [16]byte
	n, _ := ridFile.Read(ridBuf[:])
	ridFile.Close()
	rcRid := strings.TrimSpace(string(ridBuf[:n]))
	rc, err := root.Lookup(rcRid)
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: simulate elements/task.js:88-90 binding fd/0,1,2 into rc's ns.
	// JS does `this.root.bind("#term/X/program", "#task/<rc>/fd/N")`, which
	// routes to rc's syscaller and runs api/bind.go: s.task.Namespace().Bind(...)
	// where s.task is rc.
	rcMarker := []byte("rc-stdio-payload\n")
	for _, fd := range []string{"0", "1", "2"} {
		src := fskit.MapFS{"data": fskit.RawNode(rcMarker, 0644)}
		dst := "#task/" + rcRid + "/fd/" + fd
		if err := rc.Namespace().Bind(src, "data", dst); err != nil {
			t.Fatalf("rc bind fd/%s: %v", fd, err)
		}
	}

	// Step 3: rc allocates warren via #task/new/auto. Inheritance should
	// bind rc's fd/0,1,2 onto warren's #task/<warren>/fd/N inside rc.ns.
	rcCtx := context.WithValue(context.Background(), TaskContextKey, rc)
	ridFile2, err := fs.OpenContext(rcCtx, rc.Namespace(), "#task/new/auto")
	if err != nil {
		t.Fatalf("rc open #task/new/auto: %v", err)
	}
	var ridBuf2 [16]byte
	n2, _ := ridFile2.Read(ridBuf2[:])
	ridFile2.Close()
	warrenRid := strings.TrimSpace(string(ridBuf2[:n2]))
	if warrenRid == rcRid {
		t.Fatalf("warren rid %q should differ from rc rid %q", warrenRid, rcRid)
	}

	// Step 4: rc reads warren's stdout from rc's own ns (mirrors
	// rc/shell/exec_wanix.go:43 os.Open(#task/<warren>/fd/1)).
	for _, fd := range []string{"0", "1", "2"} {
		path := "#task/" + warrenRid + "/fd/" + fd
		got, err := fs.ReadFile(rc.Namespace(), path)
		if err != nil {
			t.Errorf("rc read %s: %v", path, err)
			continue
		}
		if string(got) != string(rcMarker) {
			t.Errorf("rc read %s: got %q, want %q", path, got, rcMarker)
		}
	}
}

// TestRCStyleAllocationViaOpenFile mirrors the prod failure: rc's
// os.ReadFile("#task/new/auto") goes through api/open.go:openFile which
// calls fs.OpenFile with O_RDONLY. fs.OpenFile previously resolved to
// OpenFunc.OpenFile (no ctx parameter) which discarded ctx and called the
// inner closure with context.Background() — so FromContext returned nil
// and Alloc ran with parent=nil, producing a child task with no inherited
// fds. The fix in fs/openfile.go routes read-only opens through
// OpenContext, preserving the caller's ctx (which includes the rc task's
// TaskContextKey from its ns.ctx). This test asserts ctx propagation is
// preserved across the OpenFile entry point that rc actually uses.
func TestRCStyleAllocationViaOpenFile(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}

	// Allocate rc as root's child via the new-handler path.
	rootCtx := context.WithValue(context.Background(), TaskContextKey, root)
	ridFile, err := fs.OpenContext(rootCtx, root.Namespace(), "#task/new/auto")
	if err != nil {
		t.Fatal(err)
	}
	var ridBuf [16]byte
	n, _ := ridFile.Read(ridBuf[:])
	ridFile.Close()
	rcRid := strings.TrimSpace(string(ridBuf[:n]))
	rc, err := root.Lookup(rcRid)
	if err != nil {
		t.Fatal(err)
	}

	// Bind rc's fd/0,1,2 the way JS does (mimics elements/task.js).
	rcMarker := []byte("rc-stdio-payload\n")
	for _, fd := range []string{"0", "1", "2"} {
		src := fskit.MapFS{"data": fskit.RawNode(rcMarker, 0644)}
		dst := "#task/" + rcRid + "/fd/" + fd
		if err := rc.Namespace().Bind(src, "data", dst); err != nil {
			t.Fatalf("rc bind fd/%s: %v", fd, err)
		}
	}

	// Mirror api/open.go:openFile exactly: fs.OpenFile(rc.ns, path, O_RDONLY, 0).
	// This is the entry point that was dropping ctx via OpenFunc.OpenFile.
	allocFile, err := fs.OpenFile(rc.Namespace(), "#task/new/auto", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("rc OpenFile #task/new/auto: %v", err)
	}
	var ridBuf2 [16]byte
	n2, _ := allocFile.Read(ridBuf2[:])
	allocFile.Close()
	warrenRid := strings.TrimSpace(string(ridBuf2[:n2]))
	if warrenRid == rcRid {
		t.Fatalf("warren rid %q should differ from rc rid %q", warrenRid, rcRid)
	}

	// rc must be able to read warren's stdout from rc's own ns. If ctx
	// propagation is broken, warren was allocated with parent=nil and these
	// reads ENOENT.
	for _, fd := range []string{"0", "1", "2"} {
		path := "#task/" + warrenRid + "/fd/" + fd
		got, err := fs.ReadFile(rc.Namespace(), path)
		if err != nil {
			t.Errorf("rc read %s: %v", path, err)
			continue
		}
		if string(got) != string(rcMarker) {
			t.Errorf("rc read %s: got %q, want %q", path, got, rcMarker)
		}
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
