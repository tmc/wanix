package wanix

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

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

func TestTaskFDManifestsStdioDoesNotStat(t *testing.T) {
	file := &statFailFile{nonSeekFile{info: fskit.Entry("stdin", 0200)}}
	fd := newOpenFile(file, "#task/1/fd/0", 0, true, true)

	manifest, err := fd.manifest(0)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Restorable || !manifest.Stdio {
		t.Fatalf("stdio manifest = %#v, want restorable stdio", manifest)
	}
}

func TestTaskFDManifestsStdioDoesNotWaitForRead(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	fd := newOpenFile(pipeReadFile{Reader: pr}, "#task/1/fd/0", 0, true, true)
	readStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(readStarted)
		var b [1]byte
		_, err := fd.Read(b[:])
		done <- err
	}()
	<-readStarted

	manifestDone := make(chan error, 1)
	go func() {
		manifest, err := fd.manifest(0)
		if err == nil && (!manifest.Restorable || !manifest.Stdio) {
			err = errors.New("stdio fd manifest is not restorable")
		}
		manifestDone <- err
	}()

	select {
	case err := <-manifestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdio manifest waited for blocked read")
	}

	pw.Close()
	<-done
}

type pipeReadFile struct {
	io.Reader
}

func (f pipeReadFile) Close() error {
	return nil
}

func (f pipeReadFile) Stat() (os.FileInfo, error) {
	return nil, errors.New("stat should not be called")
}

type statFailFile struct {
	nonSeekFile
}

func (f *statFailFile) Stat() (os.FileInfo, error) {
	return nil, errors.New("stat should not be called")
}

func TestTaskFDManifestsPipeFailClosed(t *testing.T) {
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
	if !errors.Is(err, migration.ErrUnrestorableFD) {
		t.Fatalf("FDManifests error = %v, want ErrUnrestorableFD", err)
	}
	if len(fds) != 1 {
		t.Fatalf("got %d fd manifests, want 1", len(fds))
	}
	got := fds[0]
	if got.FD != fd || got.Kind == "pipe" || got.Path != "pipe/data" || got.Restorable || !strings.Contains(got.Error, "file is not seekable") {
		t.Fatalf("unexpected pipe fd manifest: %#v", got)
	}
}

func TestTaskFDManifestsDirectoryRestorable(t *testing.T) {
	root, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	backing := memfs.New()
	if err := fs.WriteFile(backing, "data.txt", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(backing, ".", "tmp", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	file, err := fs.OpenFile(root.NS(), "tmp", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := root.OpenFDWithFlags(file, "tmp", os.O_RDONLY)

	fds, err := root.FDManifests()
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 1 {
		t.Fatalf("got %d fd manifests, want 1", len(fds))
	}
	got := fds[0]
	if got.FD != fd || got.Kind != "dir" || got.Path != "tmp" || got.Offset != 0 || !got.Restorable {
		t.Fatalf("unexpected directory fd manifest: %#v", got)
	}

	target, err := NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := target.NS().Bind(backing, ".", "tmp", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := target.importFDManifests(fds); err != nil {
		t.Fatal(err)
	}
	restored, _, err := target.FD(fd)
	if err != nil {
		t.Fatal(err)
	}
	info, err := restored.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("restored fd mode = %v, want directory", info.Mode())
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
	export := memfs.New()
	if err := fs.WriteFile(export, "export.txt", []byte("exported"), 0o644); err != nil {
		t.Fatal(err)
	}
	Export(source, export)
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
		if candidate == export {
			return "exportfs", nil
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
	if manifest.ExportFSID != "exportfs" {
		t.Fatalf("manifest export fs id = %q, want exportfs", manifest.ExportFSID)
	}

	targetTasks := NewTaskFS()
	restored, err := targetTasks.ImportManifest(context.Background(), manifest, nil, func(id string) (fs.FS, error) {
		if id == "rootfs" {
			return backing, nil
		}
		if id == "exportfs" {
			return export, nil
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
	restoredExport, err := restored.Export()
	if err != nil {
		t.Fatal(err)
	}
	exportData, err := fs.ReadFile(restoredExport, "export.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(exportData) != "exported" {
		t.Fatalf("restored export data = %q, want exported", exportData)
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
		Kind:       "pipe",
		Path:       "pipe/data",
		Flags:      os.O_RDWR,
		Restorable: true,
	}}
	_, err = taskfs.ImportManifest(context.Background(), manifest, nil, lookup)
	if !errors.Is(err, migration.ErrUnrestorableFD) || !strings.Contains(err.Error(), "pipe") {
		t.Fatalf("ImportManifest pipe fd error = %v, want ErrUnrestorableFD mentioning pipe", err)
	}

	manifest.FDs = []migration.FDManifest{{
		FD:         3,
		Kind:       "dir",
		Path:       "dir",
		Offset:     1,
		Restorable: true,
	}}
	_, err = taskfs.ImportManifest(context.Background(), manifest, nil, lookup)
	if !errors.Is(err, migration.ErrUnrestorableFD) || !strings.Contains(err.Error(), "directory stream offset") {
		t.Fatalf("ImportManifest directory fd error = %v, want ErrUnrestorableFD mentioning offset", err)
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

func TestTaskImportManifestRestoresRunningState(t *testing.T) {
	backing := memfs.New()
	if err := fs.WriteFile(backing, "data.txt", []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := &recordingTaskRestorer{}
	taskfs := NewTaskFS()
	taskfs.Register("restart", driver)
	manifest := migration.TaskManifest{
		ID:        "2",
		Kind:      "restart",
		State:     migration.TaskStateRunning,
		Command:   "run data.txt",
		Directory: "mnt",
		Env:       []string{"A=B"},
		Namespace: migration.NamespaceManifest{
			TaskID: "2",
			Binds: []migration.BindManifest{{
				DstPath: ".",
				SrcFSID: "rootfs",
				SrcPath: ".",
				Mode:    "replace",
				Index:   0,
				Root:    true,
			}},
		},
		FDs: []migration.FDManifest{{
			FD:         3,
			Kind:       "file",
			Path:       "data.txt",
			Flags:      os.O_RDONLY,
			Offset:     3,
			Restorable: true,
		}},
	}
	restored, err := taskfs.ImportManifest(context.Background(), manifest, nil, func(id string) (fs.FS, error) {
		if id == "rootfs" {
			return backing, nil
		}
		return nil, migration.ErrUnknownFilesystem
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(driver.manifests) != 1 || driver.manifests[0].State != migration.TaskStateRunning {
		t.Fatalf("restored manifests = %#v, want running state", driver.manifests)
	}
	if got := GetWorker(restored); got != "restored" {
		t.Fatalf("restored worker = %#v, want restored", got)
	}
	if restored.Cmd() != "run data.txt" || restored.Dir() != "mnt" {
		t.Fatalf("restored task cmd=%q dir=%q", restored.Cmd(), restored.Dir())
	}
	fd, _, err := restored.FD(3)
	if err != nil {
		t.Fatal(err)
	}
	next := make([]byte, 1)
	if _, err := fd.Read(next); err != nil {
		t.Fatal(err)
	}
	if string(next) != "d" {
		t.Fatalf("restored fd next byte = %q, want d", next)
	}
}

func TestTaskImportManifestRollsBackRunningRestoreFailure(t *testing.T) {
	driver := &recordingTaskRestorer{err: fs.ErrInvalid}
	taskfs := NewTaskFS()
	taskfs.Register("restart", driver)
	_, err := taskfs.ImportManifest(context.Background(), migration.TaskManifest{
		ID:    "1",
		Kind:  "restart",
		State: migration.TaskStateRunning,
	}, nil, func(string) (fs.FS, error) {
		return nil, migration.ErrUnknownFilesystem
	})
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("ImportManifest running restore error = %v, want ErrInvalid", err)
	}
	if _, err := taskfs.Lookup("1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lookup after failed running import = %v, want ErrNotExist", err)
	}
	next, err := taskfs.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() != "1" {
		t.Fatalf("next task id = %s, want 1", next.ID())
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

type recordingTaskRestorer struct {
	manifests []migration.TaskManifest
	err       error
}

func (d *recordingTaskRestorer) Check(*Task) bool {
	return false
}

func (d *recordingTaskRestorer) Start(*Task) error {
	return nil
}

func (d *recordingTaskRestorer) RestoreTask(t *Task, manifest migration.TaskManifest) error {
	d.manifests = append(d.manifests, manifest)
	if d.err != nil {
		return d.err
	}
	SetWorker(t, "restored")
	return nil
}
