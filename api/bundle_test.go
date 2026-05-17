package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

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
	root, _ := newBundleAPIRoot(t)
	dev := bindBundleAPIVMDevice(t, root)
	client := newBundleAPIClient(t, root)

	manifest := migration.BundleManifest{
		Version: migration.BundleManifestVersion,
		Mode:    migration.ModeMigrate,
		VMs: []migration.VMManifest{{
			ID:   "3",
			Kind: "v86",
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
			name: "unsupported vm state",
			manifest: migration.BundleManifest{
				Version: migration.BundleManifestVersion,
				Mode:    migration.ModeMigrate,
				Filesystems: []migration.FilesystemDescriptor{{
					ID:     "rootfs",
					Kind:   "memfs",
					Source: "missing",
				}},
				VMs: []migration.VMManifest{{
					ID:        "1",
					Kind:      "v86",
					StatePath: "vm.state",
				}},
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
			name: "bundle manifest",
			re:   `(?s)async\s+bundleManifest\s*\(\s*filesystems\s*=\s*\[\]\s*\).*?peer\.call\(\s*"BundleManifest"\s*,\s*\[\s*JSON\.stringify\(\s*filesystems\s*\)\s*\]\s*\)`,
		},
		{
			name: "restore bundle manifest",
			re:   `(?s)async\s+restoreBundleManifest\s*\(\s*manifest\s*\).*?typeof\s+manifest\s*!==\s*"string".*?manifest\s*=\s*JSON\.stringify\(\s*manifest\s*\).*?peer\.call\(\s*"RestoreBundleManifest"\s*,\s*\[\s*manifest\s*\]\s*\)`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("handle.js missing %s wrapper", tt.name)
		}
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
