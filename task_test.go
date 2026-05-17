package wanix

import (
	"errors"
	"os"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/vfs"
	"tractor.dev/wanix/migration"
)

func TestTaskFDManifestsRestorableOffset(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	fsys := fskit.MapFS{
		"data.txt": fskit.RawNode([]byte("abcdef")),
	}
	if err := root.NS().Bind(fsys, ".", "tmp", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}

	file, err := fs.OpenFile(root.NS(), "tmp/data.txt", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := root.OpenFDWithFlags(file, "tmp/data.txt", os.O_RDWR)

	opened, _, err := root.FD(fd)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := opened.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(opened, []byte("Z"), 0); err != nil {
		t.Fatal(err)
	}
	next := make([]byte, 1)
	if _, err := opened.Read(next); err != nil {
		t.Fatal(err)
	}
	if string(next) != "d" {
		t.Fatalf("next read after WriteAt = %q, want d", next)
	}

	fds, err := root.FDManifests()
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 1 {
		t.Fatalf("got %d fd manifests, want 1", len(fds))
	}
	got := fds[0]
	if !got.Restorable {
		t.Fatalf("fd manifest is not restorable: %#v", got)
	}
	if got.FD != fd || got.Path != "tmp/data.txt" || got.Flags != os.O_RDWR || got.Offset != 4 {
		t.Fatalf("unexpected fd manifest: %#v", got)
	}
}

func TestTaskFDManifestsFailClosed(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	file := &nonSeekFile{info: fskit.Entry("stream", 0444)}
	root.OpenFD(file, "stream")

	fds, err := root.FDManifests()
	if !errors.Is(err, migration.ErrUnrestorableFD) {
		t.Fatalf("FDManifests error = %v, want ErrUnrestorableFD", err)
	}
	if len(fds) != 1 {
		t.Fatalf("got %d fd manifests, want 1", len(fds))
	}
	if fds[0].Restorable {
		t.Fatalf("fd manifest is restorable, want fail-closed: %#v", fds[0])
	}
	if fds[0].Error == "" {
		t.Fatalf("fd manifest missing error: %#v", fds[0])
	}
}

type nonSeekFile struct {
	info fs.FileInfo
}

func (f *nonSeekFile) Read([]byte) (int, error) {
	return 0, fs.ErrClosed
}

func (f *nonSeekFile) Close() error {
	return nil
}

func (f *nonSeekFile) Stat() (fs.FileInfo, error) {
	return f.info, nil
}
