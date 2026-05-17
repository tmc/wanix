package wanix

import (
	"context"
	"errors"
	"os"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/memfs"
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

func TestTaskManifestImportRestoresNamespaceAndFDs(t *testing.T) {
	sourceTasks := NewTaskFS()
	source, err := sourceTasks.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	source.alias = "shell"
	source.cmd = "rc -c test"
	source.exit = "7"
	source.dir = "tmp"
	source.env = []string{"A=B", "C=D"}

	backing := memfs.New()
	if err := fs.WriteFile(backing, "data.txt", []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := source.NS().Bind(backing, ".", "tmp", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	file, err := fs.OpenFile(source.NS(), "tmp/data.txt", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := source.OpenFDWithFlags(file, "tmp/data.txt", os.O_RDWR)
	opened, _, err := source.FD(fd)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := opened.Read(buf); err != nil {
		t.Fatal(err)
	}

	manifest, err := source.Manifest(func(candidate fs.FS) (string, error) {
		if candidate == backing {
			return "rootfs", nil
		}
		return "", migration.ErrUnknownFilesystem
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != source.ID() || manifest.Kind != "auto" || manifest.Alias != "shell" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if manifest.Exit != "7" {
		t.Fatalf("manifest exit = %q, want 7", manifest.Exit)
	}
	if len(manifest.FDs) != 1 || manifest.FDs[0].FD != fd || manifest.FDs[0].Offset != 3 {
		t.Fatalf("manifest fds = %#v, want fd %d offset 3", manifest.FDs, fd)
	}

	targetTasks := NewTaskFS()
	restored, err := targetTasks.ImportManifest(context.Background(), manifest, nil, func(id string) (fs.FS, error) {
		if id == "rootfs" {
			return backing, nil
		}
		return nil, migration.ErrUnknownFilesystem
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID() != source.ID() || restored.Cmd() != "rc -c test" || restored.Dir() != "tmp" || restored.Alias() != "shell" {
		t.Fatalf("restored task = id %s cmd %q dir %q alias %q", restored.ID(), restored.Cmd(), restored.Dir(), restored.Alias())
	}
	exit, err := fs.ReadFile(restored, "exit")
	if err != nil {
		t.Fatal(err)
	}
	if string(exit) != "7\n" {
		t.Fatalf("restored exit = %q, want 7\\n", exit)
	}
	if got := restored.Env(); !reflectStringSlicesEqual(got, []string{"A=B", "C=D"}) {
		t.Fatalf("restored env = %#v", got)
	}
	data, err := fs.ReadFile(restored.NS(), "tmp/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcdef" {
		t.Fatalf("restored data = %q, want abcdef", data)
	}
	restoredFD, _, err := restored.FD(fd)
	if err != nil {
		t.Fatal(err)
	}
	next := make([]byte, 1)
	if _, err := restoredFD.Read(next); err != nil {
		t.Fatal(err)
	}
	if string(next) != "d" {
		t.Fatalf("restored fd next byte = %q, want d", next)
	}
	aliasData, err := fs.ReadFile(targetTasks, "shell/id")
	if err != nil {
		t.Fatal(err)
	}
	if string(aliasData) != restored.ID()+"\n" {
		t.Fatalf("alias id data = %q, want %q", aliasData, restored.ID()+"\n")
	}
	nextTask, err := targetTasks.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	if nextTask.ID() == restored.ID() {
		t.Fatalf("next allocation reused restored task id %s", restored.ID())
	}
}

func TestTaskImportManifestFailsClosedOnFDs(t *testing.T) {
	taskfs := NewTaskFS()
	lookup := func(string) (fs.FS, error) {
		return nil, migration.ErrUnknownFilesystem
	}
	manifest := migration.TaskManifest{
		ID:   "1",
		Kind: "auto",
		FDs: []migration.FDManifest{{
			FD:         3,
			Path:       "stream",
			Restorable: false,
			Error:      "file is not seekable",
		}},
	}
	_, err := taskfs.ImportManifest(context.Background(), manifest, nil, lookup)
	if !errors.Is(err, migration.ErrUnrestorableFD) {
		t.Fatalf("ImportManifest error = %v, want ErrUnrestorableFD", err)
	}
	if _, err := taskfs.Lookup("1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lookup after failed import = %v, want ErrNotExist", err)
	}

	manifest.FDs = []migration.FDManifest{{
		FD:         3,
		Path:       "missing",
		Flags:      os.O_RDWR,
		Restorable: true,
	}}
	_, err = taskfs.ImportManifest(context.Background(), manifest, nil, lookup)
	if err == nil {
		t.Fatal("ImportManifest path-backed missing fd error = nil")
	}
	if errors.Is(err, migration.ErrUnrestorableFD) {
		t.Fatalf("ImportManifest missing path error = %v, want filesystem error", err)
	}
}

func reflectStringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
