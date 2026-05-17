package wanix_test

import (
	"context"
	"errors"
	"fmt"

	wanix "tractor.dev/wanix"
	wanixfs "tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/cowfs"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/migration"
)

func ExampleRestoreBundle() {
	base := memfs.New()
	must(wanixfs.WriteFile(base, "old.txt", []byte("stale"), 0o644))
	must(wanixfs.WriteFile(base, "config.txt", []byte("base"), 0o644))

	overlay := memfs.New()
	rootfs, err := cowfs.Restore(base, overlay, ".wh")
	must(err)
	must(rootfs.Remove("old.txt"))
	must(wanixfs.WriteFile(rootfs, "state.txt", []byte("ready"), 0o644))

	rootDesc, err := rootfs.Descriptor("rootfs", "base", "overlay")
	must(err)
	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		Filesystems: []migration.FilesystemDescriptor{
			rootDesc,
		},
		Tasks: []migration.TaskManifest{{
			ID:      "1",
			Kind:    "auto",
			Alias:   "app",
			Command: "app.wasm",
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
		}},
	}

	restored, err := wanix.RestoreBundle(context.Background(), manifest, wanix.BundleRestoreOptions{
		Filesystems: map[string]wanixfs.FS{
			"base":    base,
			"overlay": overlay,
		},
	})
	must(err)

	task := restored.Tasks[0]
	state, err := wanixfs.ReadFile(task.NS(), "state.txt")
	must(err)
	_, err = wanixfs.Stat(task.NS(), "old.txt")

	fmt.Printf("task %s restored %s\n", task.Alias(), state)
	fmt.Printf("old deleted: %t\n", errors.Is(err, wanixfs.ErrNotExist))

	// Output:
	// task app restored ready
	// old deleted: true
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
