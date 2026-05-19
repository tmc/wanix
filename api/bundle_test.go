package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"tractor.dev/toolkit-go/duplex/codec"
	"tractor.dev/toolkit-go/duplex/mux"
	"tractor.dev/toolkit-go/duplex/talk"
	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/vfs"
	"tractor.dev/wanix/migration"
	"tractor.dev/wanix/vm"
)

func TestBundleFilesystemResolverExportsManifest(t *testing.T) {
	root, backing := newBundleAPIRoot(t)
	if err := fs.Mkdir(backing, "sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(backing, "sub/nested.txt", []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	descs := []migration.FilesystemDescriptor{{
		ID:     "rootfs",
		Kind:   "memfs",
		Source: "mnt",
	}, {
		ID:     "subfs",
		Kind:   "memfs",
		Source: "mnt/sub",
	}, {
		ID:     "taskfs",
		Kind:   "taskfs",
		Source: "#task",
	}, {
		ID:     "wanixfs",
		Kind:   "system",
		Source: "#wanix",
	}}
	resolver, err := bundleFilesystemResolver(root.Context(), root.NS(), descs)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := root.BundleManifest(wanix.BundleManifestOptions{
		Filesystems: descs,
		Resolve:     resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(manifest.Tasks))
	}
	foundRootfs := false
	for _, bind := range manifest.Tasks[0].Namespace.Binds {
		if bind.SrcFSID == "rootfs" && bind.DstPath == "mnt" {
			foundRootfs = true
		}
	}
	if !foundRootfs {
		t.Fatalf("manifest binds = %#v, want mnt bind from rootfs", manifest.Tasks[0].Namespace.Binds)
	}
	lookup, err := bundleFilesystemLookup(root.Context(), root.NS(), manifest.Filesystems)
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := lookup("rootfs")
	if err != nil {
		t.Fatal(err)
	}
	if !fs.Equal(fsys, backing) {
		t.Fatalf("lookup returned %T, want backing fs", fsys)
	}
	sub, err := lookup("subfs")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(sub, "nested.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "nested" {
		t.Fatalf("sub filesystem data = %q, want nested", data)
	}
}

func TestBundleFilesystemLookupRestoresTaskManifest(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	lookup, err := bundleFilesystemLookup(root.Context(), root.NS(), []migration.FilesystemDescriptor{{
		ID:     "rootfs",
		Kind:   "memfs",
		Source: "mnt",
	}})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := root.ImportBundleManifest(context.Background(), migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
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
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID() != "2" {
		t.Fatalf("tasks = %#v, want task 2", tasks)
	}
	data, err := fs.ReadFile(tasks[0].NS(), "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("restored file = %q, want hello", data)
	}
}

func TestBundleManifestRPCBoundary(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	client := newBundleAPIClient(t, root)

	descs := []migration.FilesystemDescriptor{{
		ID:     "rootfs",
		Kind:   "memfs",
		Source: "mnt",
	}, {
		ID:     "taskfs",
		Kind:   "taskfs",
		Source: "#task",
	}, {
		ID:     "wanixfs",
		Kind:   "system",
		Source: "#wanix",
	}}
	data, err := json.Marshal(descs)
	if err != nil {
		t.Fatal(err)
	}
	var manifest migration.BundleManifest
	if _, err := client.Call(context.Background(), "BundleManifest", []any{string(data)}, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != migration.BundleManifestVersion {
		t.Fatalf("manifest version = %q, want %q", manifest.Version, migration.BundleManifestVersion)
	}
	if len(manifest.Filesystems) != len(descs) || manifest.Filesystems[0].ID != "rootfs" {
		t.Fatalf("manifest filesystems = %#v, want exported descriptors", manifest.Filesystems)
	}
	if len(manifest.Tasks) != 1 {
		t.Fatalf("manifest tasks = %d, want 1", len(manifest.Tasks))
	}
}

func TestBundleTaskStatesRPCBoundary(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	client := newBundleAPIClient(t, root)

	var states []migration.TaskStatePayload
	if _, err := client.Call(context.Background(), "BundleTaskStates", []any{}, &states); err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("task states = %#v, want none on native test runtime", states)
	}
}

func TestRestoreBundleManifestRPCBoundary(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	client := newBundleAPIClient(t, root)

	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		Filesystems: []migration.FilesystemDescriptor{{
			ID:     "rootfs",
			Kind:   "memfs",
			Source: "mnt",
		}},
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
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tasks []string `json:"tasks"`
	}
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 || result.Tasks[0] != "2" {
		t.Fatalf("restored tasks = %#v, want task 2", result.Tasks)
	}
}

func TestRestoreBundleManifestRPCRestoresCowFSDescriptor(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	base := memfs.New()
	overlay := memfs.New()
	if err := fs.WriteFile(base, "base.txt", []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(overlay, "overlay.txt", []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(base, ".", "base", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(overlay, ".", "overlay", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	client := newBundleAPIClient(t, root)

	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		Filesystems: []migration.FilesystemDescriptor{{
			ID:     "basefs",
			Kind:   "memfs",
			Source: "base",
		}, {
			ID:     "overlayfs",
			Kind:   "memfs",
			Source: "overlay",
		}, {
			ID:          "cowfs",
			Kind:        "cowfs",
			Source:      "mnt",
			BaseFSID:    "basefs",
			OverlayFSID: "overlayfs",
			WhiteoutDir: ".wh",
		}},
		Tasks: []migration.TaskManifest{{
			ID:   "2",
			Kind: "auto",
			Namespace: migration.NamespaceManifest{
				TaskID: "2",
				Binds: []migration.BindManifest{{
					DstPath: "mnt",
					SrcFSID: "cowfs",
					SrcPath: ".",
					Mode:    "replace",
					Index:   0,
				}},
			},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tasks []string `json:"tasks"`
	}
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err != nil {
		t.Fatal(err)
	}
	restored, err := root.Lookup("2")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		want string
	}{
		{"mnt/base.txt", "base"},
		{"mnt/overlay.txt", "overlay"},
	} {
		data, err := fs.ReadFile(restored.NS(), tt.name)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != tt.want {
			t.Fatalf("%s = %q, want %q", tt.name, data, tt.want)
		}
	}
}

func TestBindCowFSRPCBoundary(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	base := memfs.New()
	overlay := memfs.New()
	if err := fs.Mkdir(base, "dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(base, "dir/base.txt", []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(base, ".", "base", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(overlay, ".", "overlay", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	client := newBundleAPIClient(t, root)

	var result any
	if _, err := client.Call(context.Background(), "BindCowFS", []string{"base", "overlay", "cow", ".wh"}, &result); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(root.NS(), "cow/dir/base.txt", []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(overlay, "dir/base.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "changed" {
		t.Fatalf("overlay copy = %q, want changed", data)
	}
	if err := fs.Remove(root.NS(), "cow/dir/base.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(root.NS(), "cow/dir/base.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed cow path error = %v, want ErrNotExist", err)
	}
	if _, err := client.Call(context.Background(), "BindCowFS", []string{"base", "overlay", "restored", ".wh"}, &result); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(root.NS(), "restored/dir/base.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restored cow path error = %v, want persisted ErrNotExist", err)
	}
}

func TestBindMemFSRPCBoundary(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	client := newBundleAPIClient(t, root)

	var result any
	if _, err := client.Call(context.Background(), "BindMemFS", []string{"one"}, &result); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "BindMemFS", []string{"two"}, &result); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(root.NS(), "one/file.txt", []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(root.NS(), "two/file.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("two/file.txt error = %v, want ErrNotExist", err)
	}
}

func TestRestoreBundleManifestRPCRestoresRootInPlace(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	client := newBundleAPIClient(t, root)

	manifest := rootInPlaceBundleManifest()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tasks []string `json:"tasks"`
	}
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 || result.Tasks[0] != "1" {
		t.Fatalf("restored tasks = %#v, want root task", result.Tasks)
	}
	if root.Alias() != "migrated-root" || root.Cmd() != "rc -c migrated" || root.Dir() != "mnt" {
		t.Fatalf("root state = alias %q cmd %q dir %q", root.Alias(), root.Cmd(), root.Dir())
	}
	exit, err := fs.ReadFile(root, "exit")
	if err != nil {
		t.Fatal(err)
	}
	if string(exit) != "0\n" {
		t.Fatalf("root exit = %q, want 0\\n", exit)
	}
	id, err := fs.ReadFile(root.NS(), "#task/migrated-root/id")
	if err != nil {
		t.Fatal(err)
	}
	if string(id) != "1\n" {
		t.Fatalf("root alias id = %q, want 1\\n", id)
	}
}

func TestRestoreBundleManifestRPCRollsBackRootInPlace(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	client := newBundleAPIClient(t, root)

	manifest := rootInPlaceBundleManifest()
	manifest.Tasks = append(manifest.Tasks, migration.TaskManifest{
		ID:   "2",
		Kind: "missing",
	})
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err == nil {
		t.Fatal("RestoreBundleManifest error = nil, want child task failure")
	}
	if root.Alias() != "" || root.Cmd() != "" || root.Dir() != "" {
		t.Fatalf("root state after rollback = alias %q cmd %q dir %q", root.Alias(), root.Cmd(), root.Dir())
	}
	if _, err := fs.ReadFile(root.NS(), "#task/migrated-root/id"); err == nil {
		t.Fatal("migrated-root alias survived rollback")
	}
}

func TestRestoreBundleManifestRPCRestoresVMDescriptors(t *testing.T) {
	root, backing := newBundleAPIRoot(t)
	dev := bindBundleAPIVMDevice(t, root)
	client := newBundleAPIClient(t, root)
	if err := fs.WriteFile(backing, "vm.state", []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		VMs: []migration.VMManifest{{
			ID:        "3",
			Kind:      "v86",
			StatePath: "mnt/vm.state",
			Labels: map[string]string{
				"alias": "guest",
			},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		VMs []string `json:"vms"`
	}
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.VMs) != 1 || result.VMs[0] != "3" {
		t.Fatalf("restored vms = %#v, want vm 3", result.VMs)
	}
	restored, err := dev.Lookup("3")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Alias() != "guest" || restored.Kind() != "v86" {
		t.Fatalf("restored vm = id %q kind %q alias %q", restored.ID(), restored.Kind(), restored.Alias())
	}
	if string(restored.State()) != "state" {
		t.Fatalf("restored vm state = %q, want state", restored.State())
	}
}

func TestRestoreBundleManifestRPCRestoresVMGuestFilesystem(t *testing.T) {
	root, backing := newBundleAPIRoot(t)
	dev := bindBundleAPIVMDevice(t, root)
	client := newBundleAPIClient(t, root)
	if err := fs.WriteFile(backing, "guest.txt", []byte("guest"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		Filesystems: []migration.FilesystemDescriptor{{
			ID:     "guestfs",
			Kind:   "memfs",
			Source: "mnt",
		}},
		VMs: []migration.VMManifest{{
			ID:        "3",
			Kind:      "v86",
			GuestFSID: "guestfs",
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		VMs []string `json:"vms"`
	}
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.VMs) != 1 || result.VMs[0] != "3" {
		t.Fatalf("restored vms = %#v, want vm 3", result.VMs)
	}
	restored, err := dev.Lookup("3")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Guest() == nil {
		t.Fatal("restored vm guest is nil")
	}
	deadline := time.Now().Add(time.Second)
	for {
		data, err = fs.ReadFile(root.NS(), "#vm/3/guest/guest.txt")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read restored vm guest: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(data) != "guest" {
		t.Fatalf("guest data = %q, want guest", data)
	}
}

func TestRestoreBundleManifestRPCRollsBackVMs(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	dev := bindBundleAPIVMDevice(t, root)
	client := newBundleAPIClient(t, root)

	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		VMs: []migration.VMManifest{{
			ID:   "3",
			Kind: "v86",
		}},
		Filesystems: []migration.FilesystemDescriptor{{
			ID:     "rootfs",
			Kind:   "memfs",
			Source: "missing",
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err == nil {
		t.Fatal("RestoreBundleManifest error = nil, want filesystem lookup error")
	}
	if _, err := dev.Lookup("3"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lookup rolled back vm error = %v, want ErrNotExist", err)
	}
	next, err := dev.Alloc("v86")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() != "1" {
		t.Fatalf("next vm id after rollback = %q, want 1", next.ID())
	}
}

func TestRestoreBundleManifestRPCRollsBackPartialVMs(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	dev := bindBundleAPIVMDevice(t, root)
	client := newBundleAPIClient(t, root)

	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		VMs: []migration.VMManifest{{
			ID:   "3",
			Kind: "v86",
		}, {
			ID:   "4",
			Kind: "missing",
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if _, err := client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result); err == nil {
		t.Fatal("RestoreBundleManifest error = nil, want vm import error")
	}
	if _, err := dev.Lookup("3"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lookup rolled back vm error = %v, want ErrNotExist", err)
	}
	next, err := dev.Alloc("v86")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() != "1" {
		t.Fatalf("next vm id after rollback = %q, want 1", next.ID())
	}
}

func TestRestoreBundleManifestRPCValidatesBeforeLookup(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	client := newBundleAPIClient(t, root)

	tests := []struct {
		name     string
		manifest migration.BundleManifest
		want     error
	}{
		{
			name: "invalid header",
			manifest: migration.BundleManifest{
				Version: "bad",
				Mode:    migration.ModeMigrate,
				Filesystems: []migration.FilesystemDescriptor{{
					ID:     "rootfs",
					Kind:   "memfs",
					Source: "missing",
				}},
			},
			want: migration.ErrInvalidManifest,
		},
		{
			name: "unsupported workers",
			manifest: migration.BundleManifest{
				Version: migration.BundleManifestVersion,
				Mode:    migration.ModeMigrate,
				Filesystems: []migration.FilesystemDescriptor{{
					ID:     "rootfs",
					Kind:   "memfs",
					Source: "missing",
				}},
				Workers: []migration.WorkerManifest{{ID: "worker1", Kind: "browser"}},
			},
			want: migration.ErrUnsupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.manifest)
			if err != nil {
				t.Fatal(err)
			}
			var result any
			_, err = client.Call(context.Background(), "RestoreBundleManifest", []any{string(data)}, &result)
			if err == nil || !strings.Contains(err.Error(), tt.want.Error()) {
				t.Fatalf("RestoreBundleManifest error = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "missing") {
				t.Fatalf("RestoreBundleManifest error = %v, validated after filesystem lookup", err)
			}
		})
	}
}

func TestHandleJSBundleManifestWrappers(t *testing.T) {
	data, err := os.ReadFile("handle.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "bind cowfs",
			re:   `(?s)async\s+bindCowFS\s*\(\s*base\s*,\s*overlay\s*,\s*target\s*,\s*whiteout\s*=\s*"\.wh"\s*\).*?peer\.call\(\s*"BindCowFS"\s*,\s*\[\s*base\s*,\s*overlay\s*,\s*target\s*,\s*whiteout\s*\]\s*\)`,
		},
		{
			name: "bind memfs",
			re:   `(?s)async\s+bindMemFS\s*\(\s*target\s*\).*?peer\.call\(\s*"BindMemFS"\s*,\s*\[\s*target\s*\]\s*\)`,
		},
		{
			name: "bundle manifest",
			re:   `(?s)async\s+bundleManifest\s*\(\s*filesystems\s*=\s*\[\]\s*\).*?peer\.call\(\s*"BundleManifest"\s*,\s*\[\s*JSON\.stringify\(\s*filesystems\s*\)\s*\]\s*\)`,
		},
		{
			name: "bundle vm states",
			re:   `(?s)async\s+bundleVMStates\s*\(\s*\).*?peer\.call\(\s*"BundleVMStates"\s*,\s*\[\s*\]\s*\)`,
		},
		{
			name: "bundle task states",
			re:   `(?s)async\s+bundleTaskStates\s*\(\s*\).*?peer\.call\(\s*"BundleTaskStates"\s*,\s*\[\s*\]\s*\)`,
		},
		{
			name: "restore bundle manifest",
			re:   `(?s)async\s+restoreBundleManifest\s*\(\s*manifest\s*\).*?typeof\s+manifest\s*!==\s*"string".*?manifest\s*=\s*JSON\.stringify\(\s*normalizeBundleManifest\(\s*manifest\s*\)\s*\).*?peer\.call\(\s*"RestoreBundleManifest"\s*,\s*\[\s*manifest\s*\]\s*\)`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("handle.js missing %s wrapper", tt.name)
		}
	}
}

func TestHandleJSBundleHarnessWrappers(t *testing.T) {
	data, err := os.ReadFile("handle.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "export bundle",
			re:   `(?s)async\s+exportBundle\s*\(\s*filesystems\s*=\s*\[\]\s*\).*?this\.bundleManifest\(\s*filesystems\s*\).*?this\.bundleVMStates\(\s*\).*?this\.bundleTaskStates\(\s*\).*?attachBundleVMGuests\(\s*manifest\s*\).*?task\.state_path\s*=\s*state\.state_path.*?bundleArchiveTarget\(\s*desc\s*,\s*request\s*\).*?bundleArchiveFilesystemSource\(\s*desc\s*,\s*request\s*\).*?this\.archive\(\s*source\s*\).*?filesystem_source.*?kind:\s*"task".*?return\s+\{\s*manifest\s*,\s*archives\s*,\s*states\s*\}`,
		},
		{
			name: "import bundle",
			re:   `(?s)async\s+importBundle\s*\(\s*bundle\s*\).*?normalizeBundleManifest\(\s*bundle\.manifest\s*\).*?bundleArchiveTarget\(\s*desc\s*,\s*archive\s*\).*?filesystemSource\s*=.*?archive\.filesystem_source.*?desc\.source\s*=\s*filesystemSource.*?this\.importArchive\(\s*target\s*,\s*bundleArchiveData\(\s*archive\.data\s*\)\s*\).*?bundle\.states.*?kind\s*===\s*"task".*?task\.state_path\s*=\s*target.*?this\.writeFile\(\s*target\s*,\s*bundleStateData\(\s*state\.data\s*\)\s*\).*?this\.restoreBundleManifest\(\s*manifest\s*\).*?this\.remove\(\s*target\s*\)`,
		},
		{
			name: "archive data",
			re:   `(?s)function\s+bundleArchiveData\s*\(\s*data\s*\).*?data\s+instanceof\s+Uint8Array.*?data\s+instanceof\s+ArrayBuffer.*?ArrayBuffer\.isView\(\s*data\s*\)`,
		},
		{
			name: "manifest normalization",
			re:   `(?s)function\s+normalizeBundleManifest\s*\(\s*manifest\s*\).*?typeof\s+out\.created_at\s*===\s*"number".*?toISOString\(\s*\)`,
		},
		{
			name: "filesystem helper",
			re:   `(?s)export\s+function\s+bundleFilesystems\s*\(\s*archives\s*=\s*\[\]\s*\).*?normalizeBundleFilesystem.*?systemBundleFilesystems.*?WanixBundleFilesystems`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("handle.js missing %s wrapper", tt.name)
		}
	}
}

func TestHandleJSBundleHarnessSmoke(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found")
	}
	script := `
import {WanixHandle, bundleFilesystems} from "./api/handle.js";

const h = Object.create(WanixHandle.prototype);
const calls = [];
h.logger = () => {};
h.bundleManifest = async filesystems => {
	calls.push(["bundleManifest", filesystems]);
	return {
		version: "wanix-migration-v1",
		mode: "migrate",
		filesystems: filesystems.map(({archive, ...desc}) => desc),
		tasks: [{id: "2", kind: "js", state: "running"}],
	};
};
h.bundleVMStates = async () => {
	calls.push(["bundleVMStates"]);
	return [{id: "1", kind: "v86", state_path: ".wanix-vmstate-1.bin", data: new Uint8Array([4, 5])}];
};
h.bundleTaskStates = async () => {
	calls.push(["bundleTaskStates"]);
	return [{id: "2", state_path: ".wanix-taskstate-2.bin", data: new Uint8Array([6, 7])}];
};
h.archive = async name => {
	calls.push(["archive", name]);
	return new Uint8Array([1, 2, 3]);
};
h.importArchive = async (name, data) => {
	calls.push(["importArchive", name, Array.from(data)]);
	return {entries: 1};
};
h.writeFile = async (name, data) => {
	calls.push(["writeFile", name, Array.from(data)]);
};
h.remove = async name => {
	calls.push(["remove", name]);
};
h.restoreBundleManifest = async manifest => {
	calls.push([
		"restoreBundleManifest",
		manifest.version,
		manifest.filesystems.map(desc => [desc.id, desc.source]),
		(manifest.vms || []).map(vm => [vm.id, vm.state_path, vm.guest_fs_id || ""]),
		(manifest.tasks || []).map(task => [task.id, task.state_path || ""]),
	]);
	return {tasks: []};
};

const filesystems = [
	{id: "rootfs", kind: "memfs", source: "mnt", archive: true},
	{id: "guestfs", kind: "memfs", source: "#vm/1/guest", archive: "#vm/1/guest/etc", archive_target: ".wanix-migration/vm-1-guest/etc", filesystem_source: ".wanix-migration/vm-1-guest"},
	{id: "taskfs", kind: "taskfs", source: "#task"},
];
const bundle = await h.exportBundle(filesystems);
if (bundle.archives.length !== 2 || bundle.archives[0].id !== "rootfs" || bundle.archives[1].id !== "guestfs") {
	throw new Error("unexpected archives " + JSON.stringify(bundle.archives));
}
if (bundle.states.length !== 2 || bundle.states[0].id !== "1" || bundle.states[1].id !== "2") {
	throw new Error("unexpected states " + JSON.stringify(bundle.states));
}
await h.importBundle({
	manifest: bundle.manifest,
	archives: [
		{id: "rootfs", source: "mnt", data: new ArrayBuffer(2)},
		{id: "guestfs", source: "#vm/1/guest/etc", target: ".wanix-migration/vm-1-guest/etc", filesystem_source: ".wanix-migration/vm-1-guest", data: new ArrayBuffer(2)},
	],
	states: bundle.states,
});
await h.importBundle({
	manifest: {
		version: "wanix-migration-v1",
		mode: "migrate",
		filesystems: [{id: "exportfs", kind: "memfs", source: "#task/2/export"}],
		tasks: [{id: "2", kind: "auto", export_fs_id: "exportfs"}],
	},
	archives: [{id: "exportfs", source: "#task/2/export", target: "exports/2", data: new Uint8Array([7])}],
});

const failClosed = Object.create(WanixHandle.prototype);
failClosed.logger = () => {};
failClosed.bundleManifest = async () => ({
	version: "wanix-migration-v1",
	mode: "migrate",
	tasks: [{id: "9", kind: "js", state: "running"}],
});
failClosed.bundleVMStates = async () => [];
failClosed.bundleTaskStates = async () => [];
failClosed.archive = async () => {
	throw new Error("archive should not be called");
};
try {
	await failClosed.exportBundle([]);
	throw new Error("exportBundle succeeded for running task without state");
} catch (error) {
	if (!String(error && error.message || error).includes("running task 9 missing checkpoint state")) {
		throw error;
	}
}

const got = JSON.stringify(calls);
const want = JSON.stringify([
	["bundleManifest", filesystems],
	["bundleVMStates"],
	["bundleTaskStates"],
	["archive", "mnt"],
	["archive", "#vm/1/guest/etc"],
	["importArchive", "mnt", [0, 0]],
	["importArchive", ".wanix-migration/vm-1-guest/etc", [0, 0]],
	["writeFile", ".wanix-vmstate-1.bin", [4, 5]],
	["writeFile", ".wanix-taskstate-2.bin", [6, 7]],
	["restoreBundleManifest", "wanix-migration-v1", [["rootfs", "mnt"], ["guestfs", ".wanix-migration/vm-1-guest"], ["taskfs", "#task"]], [["1", ".wanix-vmstate-1.bin", "guestfs"]], [["2", ".wanix-taskstate-2.bin"]]],
	["remove", ".wanix-taskstate-2.bin"],
	["remove", ".wanix-vmstate-1.bin"],
	["importArchive", "exports/2", [7]],
	["restoreBundleManifest", "wanix-migration-v1", [["exportfs", "exports/2"]], [], [["2", ""]]],
]);
if (got !== want) {
	throw new Error("calls = " + got + ", want " + want);
}

const rpcCalls = [];
const rpc = Object.create(WanixHandle.prototype);
rpc.logger = () => {};
rpc.peer = {
	async call(name, args) {
		rpcCalls.push([name, JSON.parse(args[0]).created_at]);
		return {value: true};
	},
};
await rpc.restoreBundleManifest({
	version: "wanix-migration-v1",
	mode: "migrate",
	created_at: 1,
});
if (JSON.stringify(rpcCalls) !== JSON.stringify([["RestoreBundleManifest", "1970-01-01T00:00:01.000Z"]])) {
	throw new Error("rpc calls = " + JSON.stringify(rpcCalls));
}

const helper = bundleFilesystems([{id: "rootfs", source: "mnt"}]);
if (JSON.stringify(helper.slice(0, 2)) !== JSON.stringify([
	{kind: "memfs", archive: true, id: "rootfs", source: "mnt"},
	{id: "taskfs", kind: "taskfs", source: "#task"},
])) {
	throw new Error("helper = " + JSON.stringify(helper));
}

const cow = bundleFilesystems([
	{id: "basefs", source: "base"},
	{id: "overlayfs", source: "overlay"},
	{id: "cowfs", kind: "cowfs", base_fs_id: "basefs", overlay_fs_id: "overlayfs", whiteout_dir: ".wh"},
]);
if (JSON.stringify(cow.slice(0, 3)) !== JSON.stringify([
	{kind: "memfs", archive: true, id: "basefs", source: "base"},
	{kind: "memfs", archive: true, id: "overlayfs", source: "overlay"},
	{id: "cowfs", kind: "cowfs", base_fs_id: "basefs", overlay_fs_id: "overlayfs", whiteout_dir: ".wh"},
])) {
	throw new Error("cow helper = " + JSON.stringify(cow));
}
`
	cmd := exec.Command("node", "--input-type=module", "-e", script)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node bundle harness smoke: %v\n%s", err, out)
	}
}

func TestBundleFilesystemRefsFailClosed(t *testing.T) {
	root, _ := newBundleAPIRoot(t)
	tests := []struct {
		name string
		desc migration.FilesystemDescriptor
		err  error
	}{
		{"missing id", migration.FilesystemDescriptor{Source: "mnt"}, migration.ErrUnknownFilesystem},
		{"duplicate id", migration.FilesystemDescriptor{ID: "rootfs", Source: "mnt"}, fs.ErrExist},
		{"missing source", migration.FilesystemDescriptor{ID: "rootfs"}, fs.ErrInvalid},
		{"subpath source", migration.FilesystemDescriptor{ID: "rootfs", Source: "mnt/file.txt"}, fs.ErrInvalid},
		{"missing path", migration.FilesystemDescriptor{ID: "rootfs", Source: "missing"}, fs.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descs := []migration.FilesystemDescriptor{tt.desc}
			if tt.name == "duplicate id" {
				descs = append(descs, tt.desc)
			}
			_, err := bundleFilesystemRefs(root.Context(), root.NS(), descs)
			if !errors.Is(err, tt.err) {
				t.Fatalf("bundleFilesystemRefs error = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestDecodeJSONArg(t *testing.T) {
	got, err := decodeJSONArg[[]migration.FilesystemDescriptor]([]any{`[{"id":"rootfs","source":"mnt"}]`}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "rootfs" || got[0].Source != "mnt" {
		t.Fatalf("decoded = %#v", got)
	}
}

func rootInPlaceBundleManifest() migration.BundleManifest {
	return migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		Filesystems: []migration.FilesystemDescriptor{{
			ID:     "rootfs",
			Kind:   "memfs",
			Source: "mnt",
		}, {
			ID:     "taskfs",
			Kind:   "taskfs",
			Source: "#task",
		}, {
			ID:     "wanixfs",
			Kind:   "system",
			Source: "#wanix",
		}},
		Tasks: []migration.TaskManifest{{
			ID:        "1",
			Kind:      "auto",
			Alias:     "migrated-root",
			Command:   "rc -c migrated",
			Exit:      "0",
			Directory: "mnt",
			Env:       []string{"A=B"},
			Namespace: migration.NamespaceManifest{
				TaskID: "1",
				Binds: []migration.BindManifest{{
					DstPath: "mnt",
					SrcFSID: "rootfs",
					SrcPath: ".",
					Mode:    "replace",
					Index:   0,
				}, {
					DstPath: "#task",
					SrcFSID: "taskfs",
					SrcPath: ".",
					Mode:    "replace",
					Index:   0,
					System:  true,
				}, {
					DstPath: "#wanix",
					SrcFSID: "wanixfs",
					SrcPath: ".",
					Mode:    "replace",
					Index:   0,
					System:  true,
				}},
			},
		}},
	}
}

func newBundleAPIRoot(t *testing.T) (*wanix.Task, fs.FS) {
	t.Helper()
	root, err := wanix.NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	backing := memfs.New()
	if err := fs.WriteFile(backing, "file.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(backing, ".", "mnt", vfs.ModeReplace); err != nil {
		t.Fatal(err)
	}
	return root, backing
}

func bindBundleAPIVMDevice(t *testing.T, root *wanix.Task) *vm.Device {
	t.Helper()
	dev := vm.New(root)
	if err := root.NS().Bind(dev, ".", "#vm"); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(memfs.New(), ".", "#vm/v86"); err != nil {
		t.Fatal(err)
	}
	return dev
}

func newBundleAPIClient(t *testing.T, root *wanix.Task) *talk.Peer {
	t.Helper()
	clientSess, serverSess := mux.Pair()
	go Responder(serverSess, root)
	client := talk.NewPeer(clientSess, codec.CBORCodec{})
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
