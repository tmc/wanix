package wanix

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/pipe"
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

func TestTaskFDManifestsPipeZeroOffsetRestorable(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	sourcePipe, _, _ := pipe.NewFS(false)
	if err := root.NS().Bind(sourcePipe, ".", "pipe", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	file, err := fs.OpenFile(root.NS(), "pipe/data", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := root.OpenFDWithFlags(file, "pipe/data", os.O_RDWR)

	fds, err := root.FDManifests()
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 1 {
		t.Fatalf("got %d fd manifests, want 1", len(fds))
	}
	got := fds[0]
	if got.FD != fd || got.Kind != "pipe" || got.Path != "pipe/data" || got.Offset != 0 || !got.Restorable {
		t.Fatalf("unexpected pipe fd manifest: %#v", got)
	}

	target, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	targetPipe, _, reader := pipe.NewFS(false)
	if err := target.NS().Bind(targetPipe, ".", "pipe", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := target.importFDManifests(fds); err != nil {
		t.Fatal(err)
	}
	restored, _, err := target.FD(fd)
	if err != nil {
		t.Fatal(err)
	}
	writer, ok := restored.(interface {
		Write([]byte) (int, error)
	})
	if !ok {
		t.Fatalf("restored pipe fd is %T, want writer", restored)
	}
	if _, err := writer.Write([]byte("pipe-ok")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("pipe-ok"))
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "pipe-ok" {
		t.Fatalf("pipe read = %q, want pipe-ok", buf[:n])
	}
}

func TestTaskFDManifestsPipeOffsetFailClosed(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	fsys, _, _ := pipe.NewFS(false)
	if err := root.NS().Bind(fsys, ".", "pipe", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	file, err := fs.OpenFile(root.NS(), "pipe/data", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := root.OpenFDWithFlags(file, "pipe/data", os.O_RDWR)
	opened, _, err := root.FD(fd)
	if err != nil {
		t.Fatal(err)
	}
	writer, ok := opened.(interface {
		Write([]byte) (int, error)
	})
	if !ok {
		t.Fatalf("pipe fd is %T, want writer", opened)
	}
	if _, err := writer.Write([]byte("busy")); err != nil {
		t.Fatal(err)
	}

	fds, err := root.FDManifests()
	if !errors.Is(err, migration.ErrUnrestorableFD) {
		t.Fatalf("FDManifests error = %v, want ErrUnrestorableFD", err)
	}
	if len(fds) != 1 {
		t.Fatalf("got %d fd manifests, want 1", len(fds))
	}
	got := fds[0]
	if got.Kind != "pipe" || got.Restorable || !strings.Contains(got.Error, "pipe stream offset") {
		t.Fatalf("unexpected pipe fd manifest: %#v", got)
	}

	target, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	targetPipe, _, _ := pipe.NewFS(false)
	if err := target.NS().Bind(targetPipe, ".", "pipe", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	err = target.importFDManifests([]migration.FDManifest{{
		FD:         fd,
		Kind:       "pipe",
		Path:       "pipe/data",
		Flags:      os.O_RDWR,
		Offset:     1,
		Restorable: true,
	}})
	if !errors.Is(err, migration.ErrUnrestorableFD) {
		t.Fatalf("importFDManifests error = %v, want ErrUnrestorableFD", err)
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
	if manifest.State != migration.TaskStateExited {
		t.Fatalf("manifest state = %q, want exited", manifest.State)
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
		Kind:       "socket",
		Path:       "socket",
		Flags:      os.O_RDWR,
		Restorable: true,
	}}
	_, err = taskfs.ImportManifest(context.Background(), manifest, nil, lookup)
	if !errors.Is(err, migration.ErrUnrestorableFD) || !strings.Contains(err.Error(), "socket") {
		t.Fatalf("ImportManifest socket fd error = %v, want ErrUnrestorableFD mentioning socket", err)
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

func TestTaskManifestState(t *testing.T) {
	taskfs := NewTaskFS()
	created, err := taskfs.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := created.Manifest(func(fs.FS) (string, error) {
		return "", migration.ErrUnknownFilesystem
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.State != migration.TaskStateCreated {
		t.Fatalf("created task state = %q, want created", manifest.State)
	}

	manifest = migration.TaskManifest{
		ID:    "2",
		Kind:  "auto",
		State: migration.TaskStateRunning,
	}
	if _, err := taskfs.ImportManifest(context.Background(), manifest, nil, func(string) (fs.FS, error) {
		return nil, migration.ErrUnknownFilesystem
	}); !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("ImportManifest running state error = %v, want ErrUnsupported", err)
	}

	manifest.State = "mystery"
	if _, err := taskfs.ImportManifest(context.Background(), manifest, nil, func(string) (fs.FS, error) {
		return nil, migration.ErrUnknownFilesystem
	}); !errors.Is(err, migration.ErrInvalidManifest) {
		t.Fatalf("ImportManifest invalid state error = %v, want ErrInvalidManifest", err)
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
