package wanix

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/cowfs"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/vfs"
	"tractor.dev/wanix/migration"
)

func TestTaskFSBundleManifestRoundTrip(t *testing.T) {
	backing := memfs.New()
	if err := fs.WriteFile(backing, "file.txt", []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := memfs.New()
	if err := fs.WriteFile(extra, "marker.txt", []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceTasks := NewTaskFS()
	source, err := sourceTasks.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	source.alias = "shell"
	source.cmd = "rc -c test"
	source.dir = "mnt"
	source.env = []string{"A=B", "C=D"}
	if err := source.NS().Bind(backing, ".", "mnt", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := source.NS().Bind(extra, ".", "extra", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	file, err := fs.OpenFile(source.NS(), "mnt/file.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := source.OpenFDWithFlags(file, "mnt/file.txt", os.O_RDONLY)
	open, _, err := source.FD(fd)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := open.Read(buf); err != nil {
		t.Fatal(err)
	}

	manifest, err := sourceTasks.BundleManifest(BundleManifestOptions{
		Filesystems: []migration.FilesystemDescriptor{{
			ID:   "rootfs",
			Kind: "memfs",
		}, {
			ID:   "extra",
			Kind: "memfs",
		}},
		Resolve: func(candidate fs.FS) (string, error) {
			if candidate == backing {
				return "rootfs", nil
			}
			if candidate == extra {
				return "extra", nil
			}
			return "", migration.ErrUnknownFilesystem
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != BundleManifestVersion || manifest.Mode != migration.ModeMigrate || len(manifest.Tasks) != 1 {
		t.Fatalf("manifest header/tasks = %#v", manifest)
	}
	restored, err := RestoreBundle(context.Background(), manifest, BundleRestoreOptions{
		Filesystems: map[string]fs.FS{"rootfs": backing, "extra": extra},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := restored.Tasks[0]
	if task.Alias() != "shell" || task.Cmd() != "rc -c test" || task.Dir() != "mnt" {
		t.Fatalf("restored metadata alias=%q cmd=%q dir=%q", task.Alias(), task.Cmd(), task.Dir())
	}
	if !reflect.DeepEqual(task.Env(), []string{"A=B", "C=D"}) {
		t.Fatalf("restored env = %#v", task.Env())
	}
	data, err := fs.ReadFile(task.NS(), "extra/marker.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "extra" {
		t.Fatalf("restored extra bind = %q, want extra", data)
	}
	restoredFD, _, err := task.FD(fd)
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
}

func TestTaskFSBundleManifestOrdersTasks(t *testing.T) {
	taskfs := NewTaskFS()
	for i := 0; i < 10; i++ {
		if _, err := taskfs.Alloc("auto", nil); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := taskfs.BundleManifest(BundleManifestOptions{
		Resolve: func(fs.FS) (string, error) {
			return "", migration.ErrUnknownFilesystem
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, task := range manifest.Tasks {
		got = append(got, task.ID)
	}
	want := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task order = %#v, want %#v", got, want)
	}
}

func TestTaskFSBundleManifestConcurrentAlloc(t *testing.T) {
	taskfs := NewTaskFS()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errc := make(chan error, 2)
	manifestOpts := BundleManifestOptions{
		Resolve: func(fs.FS) (string, error) {
			return "", migration.ErrUnknownFilesystem
		},
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			if _, err := taskfs.Alloc("auto", nil); err != nil {
				errc <- err
				return
			}
		}
		errc <- nil
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			if _, err := taskfs.BundleManifest(manifestOpts); err != nil {
				errc <- err
				return
			}
		}
		errc <- nil
	}()
	close(start)
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskFSBundleManifestFailsClosed(t *testing.T) {
	taskfs := NewTaskFS()
	task, err := taskfs.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := BundleManifestOptions{
		Resolve: func(fs.FS) (string, error) {
			return "", migration.ErrUnknownFilesystem
		},
	}
	SetWorker(task, struct{}{})
	if _, err := taskfs.BundleManifest(opts); !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("BundleManifest worker error = %v, want ErrUnsupported", err)
	}
	SetWorker(task, nil)
	Export(task, memfs.New())
	if _, err := taskfs.BundleManifest(opts); !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("BundleManifest export error = %v, want ErrUnsupported", err)
	}
	if _, err := taskfs.BundleManifest(BundleManifestOptions{}); !errors.Is(err, migration.ErrUnknownFilesystem) {
		t.Fatalf("BundleManifest nil resolver error = %v, want ErrUnknownFilesystem", err)
	}
}

func TestRestoreBundleRestoresCowfsTaskAndFD(t *testing.T) {
	base := memfs.New()
	if err := fs.WriteFile(base, "a.txt", []byte("deleted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(base, "c.txt", []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := memfs.New()
	source, err := cowfs.Restore(base, overlay, ".wh")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Remove("a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := source.Rename("c.txt", "d.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(source, "b.txt", []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	cowDesc, err := source.Descriptor("cow1", "base1", "overlay1")
	if err != nil {
		t.Fatal(err)
	}

	manifest := migration.BundleManifest{
		Version: "wanix-migration-v1",
		Mode:    migration.ModeMigrate,
		Filesystems: []migration.FilesystemDescriptor{
			cowDesc,
		},
		Tasks: []migration.TaskManifest{{
			ID:        "1",
			Kind:      "auto",
			Alias:     "shell",
			Command:   "rc -c test",
			Directory: "mnt",
			Env:       []string{"A=B"},
			Namespace: migration.NamespaceManifest{
				TaskID: "1",
				Binds: []migration.BindManifest{{
					DstPath: "mnt",
					SrcFSID: "cow1",
					SrcPath: ".",
					Mode:    "replace",
					Index:   0,
				}},
			},
			FDs: []migration.FDManifest{{
				FD:         3,
				Path:       "mnt/d.txt",
				Flags:      os.O_RDONLY,
				Offset:     3,
				Restorable: true,
			}},
		}},
	}
	restored, err := RestoreBundle(context.Background(), manifest, BundleRestoreOptions{
		Filesystems: map[string]fs.FS{
			"base1":    base,
			"overlay1": overlay,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Tasks) != 1 {
		t.Fatalf("got %d restored tasks, want 1", len(restored.Tasks))
	}
	task := restored.Tasks[0]
	if task.ID() != "1" || task.Alias() != "shell" || task.Cmd() != "rc -c test" || task.Dir() != "mnt" {
		t.Fatalf("restored task = id %s alias %q cmd %q dir %q", task.ID(), task.Alias(), task.Cmd(), task.Dir())
	}
	if _, err := fs.Stat(task.NS(), "mnt/a.txt"); !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
		t.Fatalf("mnt/a.txt should remain deleted, got %v", err)
	}
	data, err := fs.ReadFile(task.NS(), "mnt/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "overlay" {
		t.Fatalf("mnt/b.txt = %q, want overlay", data)
	}
	data, err = fs.ReadFile(task.NS(), "mnt/d.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcdef" {
		t.Fatalf("mnt/d.txt = %q, want abcdef", data)
	}
	fd, _, err := task.FD(3)
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
	aliasData, err := fs.ReadFile(restored.TaskFS, "shell/id")
	if err != nil {
		t.Fatal(err)
	}
	if string(aliasData) != "1\n" {
		t.Fatalf("alias id = %q, want 1\\n", aliasData)
	}
	if _, ok := restored.Filesystems["cow1"]; !ok {
		t.Fatal("restored filesystem registry missing cow1")
	}
	if _, ok := restored.Filesystems["base1"]; !ok {
		t.Fatal("restored filesystem registry missing external base1")
	}
}

func TestRestoreBundleRestoresVMDescriptors(t *testing.T) {
	manifest := migration.BundleManifest{
		VMs: []migration.VMManifest{{
			ID:   "7",
			Kind: "v86",
			Labels: map[string]string{
				"alias": "guest",
			},
		}},
	}
	var restoredVMs []migration.VMManifest
	restored, err := RestoreBundle(context.Background(), manifest, BundleRestoreOptions{
		RestoreVM: func(ctx context.Context, manifest migration.VMManifest) (string, func(), error) {
			if ctx == nil {
				t.Fatal("RestoreVM context is nil")
			}
			restoredVMs = append(restoredVMs, manifest)
			return manifest.ID, func() {
				t.Fatal("rollback called after successful restore")
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.VMs, []string{"7"}) {
		t.Fatalf("restored VMs = %#v, want 7", restored.VMs)
	}
	if len(restoredVMs) != 1 || restoredVMs[0].Kind != "v86" || restoredVMs[0].Labels["alias"] != "guest" {
		t.Fatalf("RestoreVM manifests = %#v, want v86 guest", restoredVMs)
	}
}

func TestRestoreBundleRollsBackVMsOnTaskFailure(t *testing.T) {
	manifest := migration.BundleManifest{
		VMs: []migration.VMManifest{{
			ID:   "7",
			Kind: "v86",
		}},
		Tasks: []migration.TaskManifest{{
			ID:   "1",
			Kind: "missing",
		}},
	}
	var rolledBack []string
	_, err := RestoreBundle(context.Background(), manifest, BundleRestoreOptions{
		RestoreVM: func(ctx context.Context, manifest migration.VMManifest) (string, func(), error) {
			return manifest.ID, func() {
				rolledBack = append(rolledBack, manifest.ID)
			}, nil
		},
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("RestoreBundle error = %v, want ErrNotExist", err)
	}
	if !reflect.DeepEqual(rolledBack, []string{"7"}) {
		t.Fatalf("rolled back VMs = %#v, want 7", rolledBack)
	}
}

func TestTaskFSImportBundleManifest(t *testing.T) {
	backing := memfs.New()
	if err := fs.WriteFile(backing, "file.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskfs := NewTaskFS()
	tasks, err := taskfs.ImportBundleManifest(context.Background(), migration.BundleManifest{
		Tasks: []migration.TaskManifest{{
			ID:   "2",
			Kind: "auto",
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
		}},
	}, nil, func(id string) (fs.FS, error) {
		if id == "rootfs" {
			return backing, nil
		}
		return nil, migration.ErrUnknownFilesystem
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID() != "2" {
		t.Fatalf("tasks = %#v, want task id 2", tasks)
	}
	data, err := fs.ReadFile(tasks[0].NS(), "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("restored file = %q, want hello", data)
	}
}

func TestTaskFSImportBundleManifestRollsBackOnFailure(t *testing.T) {
	backing := memfs.New()
	if err := fs.WriteFile(backing, "file.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskfs := NewTaskFS()
	manifest := migration.BundleManifest{
		Tasks: []migration.TaskManifest{
			{
				ID:    "1",
				Kind:  "auto",
				Alias: "shell",
				Namespace: migration.NamespaceManifest{
					TaskID: "1",
					Binds: []migration.BindManifest{{
						DstPath: ".",
						SrcFSID: "rootfs",
						SrcPath: ".",
						Mode:    "replace",
						Index:   0,
						Root:    true,
					}},
				},
			},
			{
				ID:   "2",
				Kind: "auto",
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
					Path:       "file.txt",
					Restorable: false,
					Error:      "unsupported descriptor",
				}},
			},
		},
	}
	_, err := taskfs.ImportBundleManifest(context.Background(), manifest, nil, func(id string) (fs.FS, error) {
		if id == "rootfs" {
			return backing, nil
		}
		return nil, migration.ErrUnknownFilesystem
	})
	if !errors.Is(err, migration.ErrUnrestorableFD) {
		t.Fatalf("ImportBundleManifest error = %v, want ErrUnrestorableFD", err)
	}
	if _, err := taskfs.Lookup("1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("taskfs.Lookup(1) error = %v, want ErrNotExist", err)
	}
	if _, err := fs.ReadFile(taskfs, "shell/id"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("alias shell/id error = %v, want ErrNotExist", err)
	}
	next, err := taskfs.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() != "1" {
		t.Fatalf("next task id = %s, want 1", next.ID())
	}
}

func TestRestoreBundleRollsBackExistingTaskFSOnFailure(t *testing.T) {
	taskfs := NewTaskFS()
	_, err := RestoreBundle(context.Background(), migration.BundleManifest{
		Tasks: []migration.TaskManifest{
			{ID: "1", Kind: "auto", Alias: "shell"},
			{ID: "2", Kind: "missing"},
		},
	}, BundleRestoreOptions{TaskFS: taskfs})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("RestoreBundle error = %v, want ErrNotExist", err)
	}
	if _, err := taskfs.Lookup("1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("taskfs.Lookup(1) error = %v, want ErrNotExist", err)
	}
	if _, err := fs.ReadFile(taskfs, "shell/id"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("alias shell/id error = %v, want ErrNotExist", err)
	}
	next, err := taskfs.Alloc("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() != "1" {
		t.Fatalf("next task id = %s, want 1", next.ID())
	}
}

func TestRestoreBundleFailsClosedForUnsupportedResources(t *testing.T) {
	_, err := RestoreBundle(context.Background(), migration.BundleManifest{
		VMs: []migration.VMManifest{{ID: "vm1", Kind: "v86"}},
	}, BundleRestoreOptions{})
	if !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("RestoreBundle VM error = %v, want ErrUnsupported", err)
	}

	_, err = RestoreBundle(context.Background(), migration.BundleManifest{
		VMs: []migration.VMManifest{{ID: "1", Kind: "v86", StatePath: "vm.state"}},
	}, BundleRestoreOptions{
		RestoreVM: func(context.Context, migration.VMManifest) (string, func(), error) {
			t.Fatal("RestoreVM called for unsupported VM state")
			return "", nil, nil
		},
	})
	if !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("RestoreBundle VM state error = %v, want ErrUnsupported", err)
	}

	_, err = RestoreBundle(context.Background(), migration.BundleManifest{
		Workers: []migration.WorkerManifest{{ID: "worker1", Kind: "browser"}},
	}, BundleRestoreOptions{})
	if !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("RestoreBundle worker error = %v, want ErrUnsupported", err)
	}
}

func TestRestoreBundleChecksUnsupportedBeforeFilesystems(t *testing.T) {
	_, err := RestoreBundle(context.Background(), migration.BundleManifest{
		Filesystems: []migration.FilesystemDescriptor{{
			ID:          "cow1",
			Kind:        cowfs.FilesystemKind,
			BaseFSID:    "missing-base",
			OverlayFSID: "missing-overlay",
		}},
		VMs: []migration.VMManifest{{ID: "vm1", Kind: "v86"}},
	}, BundleRestoreOptions{})
	if !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("RestoreBundle error = %v, want ErrUnsupported", err)
	}
}

func TestTaskFSImportBundleManifestChecksUnsupportedBeforeFilesystems(t *testing.T) {
	taskfs := NewTaskFS()
	called := false
	_, err := taskfs.ImportBundleManifest(context.Background(), migration.BundleManifest{
		Filesystems: []migration.FilesystemDescriptor{{
			ID:   "rootfs",
			Kind: "memfs",
		}},
		Workers: []migration.WorkerManifest{{ID: "worker1", Kind: "browser"}},
	}, nil, func(id string) (fs.FS, error) {
		called = true
		return memfs.New(), nil
	})
	if !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("ImportBundleManifest error = %v, want ErrUnsupported", err)
	}
	if called {
		t.Fatal("filesystem lookup was called before unsupported resources were rejected")
	}
}

func TestRestoreBundleRejectsMissingFilesystems(t *testing.T) {
	_, err := RestoreBundle(context.Background(), migration.BundleManifest{
		Filesystems: []migration.FilesystemDescriptor{{ID: "rootfs", Kind: "memfs"}},
	}, BundleRestoreOptions{})
	if !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("RestoreBundle missing fs error = %v, want ErrUnsupported", err)
	}

	_, err = RestoreBundle(context.Background(), migration.BundleManifest{
		Filesystems: []migration.FilesystemDescriptor{{
			ID:          "cow1",
			Kind:        cowfs.FilesystemKind,
			BaseFSID:    "base1",
			OverlayFSID: "missing",
		}},
	}, BundleRestoreOptions{Filesystems: map[string]fs.FS{"base1": memfs.New()}})
	if !errors.Is(err, migration.ErrUnknownFilesystem) {
		t.Fatalf("RestoreBundle missing cowfs layer error = %v, want ErrUnknownFilesystem", err)
	}
}

func TestRestoreBundleRestoresCowfsFromDescriptor(t *testing.T) {
	_, err := RestoreBundle(context.Background(), migration.BundleManifest{
		Filesystems: []migration.FilesystemDescriptor{{
			ID:          "cow1",
			Kind:        cowfs.FilesystemKind,
			BaseFSID:    "missing-base",
			OverlayFSID: "missing-overlay",
		}},
	}, BundleRestoreOptions{Filesystems: map[string]fs.FS{"cow1": memfs.New()}})
	if !errors.Is(err, migration.ErrUnknownFilesystem) {
		t.Fatalf("RestoreBundle external cowfs error = %v, want ErrUnknownFilesystem", err)
	}
}

func TestRestoreBundleRejectsDuplicateFilesystemIDs(t *testing.T) {
	_, err := RestoreBundle(context.Background(), migration.BundleManifest{
		Filesystems: []migration.FilesystemDescriptor{
			{ID: "rootfs", Kind: "memfs"},
			{ID: "rootfs", Kind: "memfs"},
		},
	}, BundleRestoreOptions{})
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("RestoreBundle duplicate fs error = %v, want ErrExist", err)
	}
}
