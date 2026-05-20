package vfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"tractor.dev/wanix/fs"

	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/migration"
)

func TestNamespace(t *testing.T) {
	// Create a test filesystem
	testFS := fstest.MapFS{
		"file1.txt":       {Data: []byte("content1")},
		"dir/file2.txt":   {Data: []byte("content2")},
		"dir/subdir/file": {Data: []byte("content3")},
	}

	// Create and initialize namespace
	ns := New(context.Background())

	// Test file binding
	ns.Bind(testFS, "file1.txt", "bound-file.txt", ModeReplace)

	content, err := fs.ReadFile(ns, "bound-file.txt")
	if err != nil {
		t.Fatalf("Failed to open bound file: %v", err)
	}
	if string(content) != "content1" {
		t.Errorf("Expected content1, got %s", string(content))
	}

	// Test directory binding
	ns.Bind(testFS, ".", "bound-dir", ModeReplace)
	content, err = fs.ReadFile(ns, "bound-dir/dir/file2.txt")
	if err != nil {
		t.Fatalf("Failed to open file in bound directory: %v", err)
	}
	if string(content) != "content2" {
		t.Errorf("Expected content2, got %s", string(content))
	}

	// Test ReadDir
	entries, err := fs.ReadDir(ns, "bound-dir/dir")
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}
	if len(entries) != 2 { // file2.txt and subdir
		t.Errorf("Expected 2 entries, got %d", len(entries))
	}

	// Test file not found
	_, err = ns.Open("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestBindRawPreservesAllocator(t *testing.T) {
	ns := New(context.Background())
	if err := ns.BindRaw(&memfs.Allocator{}, ".", "#ramfs", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(ns, "#ramfs", "one", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(ns, "#ramfs", "two", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(ns, "one/file.txt", []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(ns, "two/file.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("two/file.txt error = %v, want ErrNotExist", err)
	}
}

func TestBindAllocatesDirectAllocator(t *testing.T) {
	ns := New(context.Background())
	if err := ns.Bind(&memfs.Allocator{}, ".", "mnt", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(ns, "mnt/file.txt", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveFS(t *testing.T) {
	ns := New(context.Background())

	subFS := fskit.MapFS{
		"subfile":     fskit.RawNode([]byte("rootsub")),
		"subdir/file": fskit.RawNode([]byte("subdirfile")),
	}

	rootFS := fskit.MapFS{
		"rootfile": fskit.RawNode([]byte("rootfile")),
		"rootsub":  subFS,
	}

	bindsubFS := fskit.MapFS{
		"bindfile": fskit.RawNode([]byte("bindfile")),
		"bindsub":  subFS,
	}

	ns.Bind(rootFS, ".", ".", ModeAfter)
	ns.Bind(bindsubFS, ".", "bind", ModeAfter)

	subfs, _, err := ns.ResolveFS(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(subfs, rootFS) {
		t.Fatal("ResolveFS(.) is not rootFS")
	}

	subfs, _, err = ns.ResolveFS(context.Background(), "bind")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(subfs, bindsubFS) {
		t.Fatal("ResolveFS(bind) is not bindsubFS")
	}

	// requires fskit.MapFS to have proper ResolveFS() implementation
	var rname string
	subfs, rname, err = ns.ResolveFS(context.Background(), "bind/bindsub/subfile")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(subfs, subFS) {
		t.Fatalf("ResolveFS(bind/bindsub/subfile) is not subFS: %T %s", subfs, rname)
	}

	subfs, rname, err = ns.ResolveFS(context.Background(), "rootsub/subdir")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(subfs, subFS) {
		t.Fatalf("ResolveFS(rootsub/subdir) is not subFS: %s %T", rname, subfs)
	}
}

func TestFileBindOverRootBind(t *testing.T) {
	abfs := fskit.MapFS{
		"a": fskit.RawNode([]byte("content1")),
		"b": fskit.RawNode([]byte("content2")),
	}

	cfs := fskit.MapFS{
		"c": abfs,
	}

	ns := New(context.Background())
	if err := ns.Bind(abfs, ".", ".", ModeAfter); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(cfs, "c", "c", ModeAfter); err != nil {
		t.Fatal(err)
	}

	e, err := fs.ReadDir(ns, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 3 {
		// a, b, c
		t.Fatalf("unexpected number of entries: %v", len(e))
	}
}

func TestRecursiveSubpathBind(t *testing.T) {
	ns := New(context.Background())

	loopFS := fskit.MapFS{
		"ctl": fskit.RawNode([]byte("content1")),
		"ns":  ns,
	}

	rootFS := fskit.MapFS{
		"inner": loopFS,
	}

	ns.Bind(rootFS, ".", ".", ModeAfter)

	_, err := fs.StatContext(context.Background(), ns, ".")
	if err != nil {
		t.Fatal(err)
	}
}

func TestHiddenSelfBind(t *testing.T) {
	abFS := fskit.MapFS{
		"a": fskit.RawNode([]byte("content1")),
		"b": fskit.RawNode([]byte("content2")),
	}

	mfs := fskit.MapFS{
		"one":  abFS,
		"#two": abFS,
	}

	ns := New(context.Background())
	if err := ns.Bind(mfs, ".", ".", ModeAfter); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(ns, "#two", "two", ModeAfter); err != nil {
		t.Fatal(err)
	}

	e, err := fs.ReadDir(ns, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 2 {
		// one, two
		t.Fatal("unexpected number of non-hidden entries")
	}

	e, err = fs.ReadDir(ns, "two")
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 2 {
		// a, b
		t.Fatal("unexpected number of non-hidden entries")
	}

	_, err = fs.Stat(ns, "two/a")
	if err != nil {
		t.Fatal(err)
	}
}

func TestNamespaceHidden(t *testing.T) {
	testFS := fstest.MapFS{
		"a":  {Data: []byte("content1")},
		"b":  {Data: []byte("content2")},
		"#c": {Data: []byte("hidden")},
	}

	ns := New(context.Background())
	ns.Bind(testFS, ".", "#foo", ModeReplace)

	e, _ := fs.ReadDir(ns, ".")
	if len(e) != 0 {
		t.Fatal("expected empty root dir listing")
	}

	e, _ = fs.ReadDir(ns, "#foo")
	if len(e) != 2 {
		t.Fatal("expected only 2 files in #foo dir listing")
	}

	b, err := fs.ReadFile(ns, "#foo/#c")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hidden" {
		t.Fatal("unexpected hidden file contents")
	}

}

func TestUnionBinding(t *testing.T) {
	// Create a test filesystem
	fs1 := fstest.MapFS{
		"file1.txt":       {Data: []byte("content1")},
		"dir/file2.txt":   {Data: []byte("content2")},
		"dir/subdir/file": {Data: []byte("content3")},
	}
	fs2 := fstest.MapFS{
		"fs2.txt": {Data: []byte("fs2")},
	}

	// Create namespace with union binding at root and in dir
	ns := New(context.Background())
	ns.Bind(fs1, ".", ".", ModeAfter)
	ns.Bind(fs2, ".", ".", ModeAfter)
	ns.Bind(fs2, ".", "dir", ModeAfter)

	// Test ReadDir in root
	entries, err := fs.ReadDir(ns, ".")
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}
	if len(entries) != 3 { // file1.txt, fs2.txt, and dir
		t.Errorf("Expected 3 entries, got %d", len(entries))
	}

	// Test ReadDir in dir
	entries, err = fs.ReadDir(ns, "dir")
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}
	if len(entries) != 3 { // file2.txt, fs2.txt, and subdir
		t.Errorf("Expected 3 entries, got %d", len(entries))
	}

	for _, e := range entries {
		fi, err := fs.Stat(ns, path.Join("dir", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Name() != e.Name() {
			t.Fatalf("expected name %q, got %q", e.Name(), fi.Name())
		}
	}
}

func TestBindingModes(t *testing.T) {
	// Create test filesystems
	fs1 := fstest.MapFS{"file.txt": {Data: []byte("fs1")}}
	fs2 := fstest.MapFS{"file.txt": {Data: []byte("fs2")}}

	// Test replace mode
	ns := New(context.Background())
	ns.Bind(fs1, ".", "test", ModeReplace)
	ns.Bind(fs2, ".", "test", ModeReplace)

	content, err := fs.ReadFile(ns, "test/file.txt")
	if err != nil {
		t.Fatalf("Failed to open bound file: %v", err)
	}
	if string(content) != "fs2" {
		t.Errorf("Expected fs2, got %s", string(content))
	}

	// Test after mode (default)
	ns = New(context.Background())
	ns.Bind(fs2, ".", "test", ModeAfter)
	ns.Bind(fs1, ".", "test", ModeAfter)

	content, err = fs.ReadFile(ns, "test/file.txt")
	if err != nil {
		t.Fatalf("Failed to open bound file: %v", err)
	}
	if string(content) != "fs1" {
		t.Errorf("Expected fs1, got %s", string(content))
	}

	// Test before mode
	ns = New(context.Background())
	ns.Bind(fs1, ".", "test", ModeReplace)
	ns.Bind(fs2, ".", "test", ModeBefore)

	content, err = fs.ReadFile(ns, "test/file.txt")
	if err != nil {
		t.Fatalf("Failed to open bound file: %v", err)
	}
	if string(content) != "fs1" {
		t.Errorf("Expected fs1, got %s", string(content))
	}

}

func TestExportManifestBindingOrder(t *testing.T) {
	a := newManifestFS("a")
	b := newManifestFS("b")
	c := newManifestFS("c")

	ns := New(context.Background())
	if err := ns.Bind(a, ".", "mnt", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(b, ".", "mnt", ModeAfter); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(c, ".", "mnt", ModeBefore); err != nil {
		t.Fatal(err)
	}

	manifest, err := ns.ExportManifest("task1", manifestResolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Binds) != 3 {
		t.Fatalf("got %d binds, want 3: %#v", len(manifest.Binds), manifest.Binds)
	}
	wantIDs := []string{"b", "a", "c"}
	wantModes := []string{"after", "replace", "before"}
	for i, bind := range manifest.Binds {
		if bind.SrcFSID != wantIDs[i] || bind.Mode != wantModes[i] || bind.Index != i {
			t.Fatalf("bind %d = %#v, want id %q mode %q index %d", i, bind, wantIDs[i], wantModes[i], i)
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "manifestFS") {
		t.Fatalf("manifest serialized filesystem implementation detail: %s", data)
	}
}

func TestExportManifestRootAndSystemBinds(t *testing.T) {
	root := newManifestFS("root")
	task := newManifestFS("task")

	ns := New(context.Background())
	if err := ns.Bind(root, ".", ".", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(task, ".", "#task", ModeReplace); err != nil {
		t.Fatal(err)
	}

	manifest, err := ns.ExportManifest("task1", manifestResolver)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]migration.BindManifest{}
	for _, bind := range manifest.Binds {
		seen[bind.DstPath] = bind
	}
	if !seen["."].Root || seen["."].System {
		t.Fatalf("root bind shape = %#v", seen["."])
	}
	if !seen["#task"].System || seen["#task"].Root {
		t.Fatalf("system bind shape = %#v", seen["#task"])
	}
}

func TestExportManifestSkipsTaskSelfBind(t *testing.T) {
	root := newManifestFS("root")
	task := newManifestFS("task")

	ns := New(context.Background())
	if err := ns.Bind(root, ".", ".", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(task, ".", "#task/self", ModeReplace); err != nil {
		t.Fatal(err)
	}

	manifest, err := ns.ExportManifest("task1", func(candidate fs.FS) (string, error) {
		if candidate == root {
			return "root", nil
		}
		return "", migration.ErrUnknownFilesystem
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bind := range manifest.Binds {
		if bind.DstPath == "#task/self" {
			t.Fatalf("manifest includes dynamic self bind: %#v", manifest.Binds)
		}
	}
}

func TestExportManifestSkipsTaskRuntimeBinds(t *testing.T) {
	root := newManifestFS("root")
	term := newManifestFS("term")

	ns := New(context.Background())
	if err := ns.Bind(root, ".", ".", ModeReplace); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []string{
		"#task/self/term",
		"#task/2/term",
		"#task/source-rc/term",
		"#task/2/fd/0",
	} {
		if err := ns.Bind(term, ".", dst, ModeReplace); err != nil {
			t.Fatal(err)
		}
	}

	manifest, err := ns.ExportManifest("task1", func(candidate fs.FS) (string, error) {
		if candidate == root {
			return "root", nil
		}
		return "", migration.ErrUnknownFilesystem
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bind := range manifest.Binds {
		if strings.HasPrefix(bind.DstPath, "#task/") {
			t.Fatalf("manifest includes dynamic task bind: %#v", manifest.Binds)
		}
	}
}

func TestExportManifestUnknownFilesystem(t *testing.T) {
	ns := New(context.Background())
	if err := ns.Bind(newManifestFS("known"), ".", "mnt", ModeReplace); err != nil {
		t.Fatal(err)
	}
	_, err := ns.ExportManifest("task1", func(fs.FS) (string, error) {
		return "", nil
	})
	if !errors.Is(err, migration.ErrUnknownFilesystem) {
		t.Fatalf("ExportManifest error = %v, want ErrUnknownFilesystem", err)
	}
}

func TestImportManifestRebuildsBindingOrder(t *testing.T) {
	a := newNamespaceImportFS("a")
	b := newNamespaceImportFS("b")
	c := newNamespaceImportFS("c")
	filesystems := map[string]fs.FS{
		"a": a,
		"b": b,
		"c": c,
	}

	ns := New(context.Background())
	if err := ns.Bind(a, ".", "mnt", ModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(b, ".", "mnt", ModeAfter); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(c, ".", "mnt", ModeBefore); err != nil {
		t.Fatal(err)
	}
	manifest, err := ns.ExportManifest("task1", namespaceImportResolver)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := manifest
	shuffled.Binds = []migration.BindManifest{
		manifest.Binds[2],
		manifest.Binds[0],
		manifest.Binds[1],
	}

	imported, err := ImportManifest(context.Background(), shuffled, namespaceImportLookup(filesystems))
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(imported, "mnt/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "b" {
		t.Fatalf("imported mnt/file.txt = %q, want first after bind b", got)
	}
	roundTrip, err := imported.ExportManifest("task1", namespaceImportResolver)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, manifest) {
		t.Fatalf("round trip manifest = %#v, want %#v", roundTrip, manifest)
	}
}

func TestImportManifestPreservesMultiDestinationIndexes(t *testing.T) {
	filesystems := map[string]fs.FS{
		"root": newNamespaceImportFS("root"),
		"task": newNamespaceImportFS("task"),
		"a":    newNamespaceImportFS("a"),
		"b":    newNamespaceImportFS("b"),
		"c":    newNamespaceImportFS("c"),
	}
	manifest := migration.NamespaceManifest{
		TaskID: "task1",
		Binds: []migration.BindManifest{
			{DstPath: "mnt", SrcFSID: "c", SrcPath: ".", Mode: "before", Index: 2},
			{DstPath: ".", SrcFSID: "root", SrcPath: ".", Mode: "replace", Index: 0, Root: true},
			{DstPath: "mnt", SrcFSID: "b", SrcPath: ".", Mode: "after", Index: 0},
			{DstPath: "#task", SrcFSID: "task", SrcPath: ".", Mode: "replace", Index: 0, System: true},
			{DstPath: "mnt", SrcFSID: "a", SrcPath: ".", Mode: "replace", Index: 1},
		},
	}
	imported, err := ImportManifest(context.Background(), manifest, namespaceImportLookup(filesystems))
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(imported, "mnt/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "b" {
		t.Fatalf("imported mnt/file.txt = %q, want first indexed bind b", got)
	}
	roundTrip, err := imported.ExportManifest("task1", namespaceImportResolver)
	if err != nil {
		t.Fatal(err)
	}
	want := migration.NamespaceManifest{
		TaskID: "task1",
		Binds: []migration.BindManifest{
			{DstPath: "#task", SrcFSID: "task", SrcPath: ".", Mode: "replace", Index: 0, System: true},
			{DstPath: ".", SrcFSID: "root", SrcPath: ".", Mode: "replace", Index: 0, Root: true},
			{DstPath: "mnt", SrcFSID: "b", SrcPath: ".", Mode: "after", Index: 0},
			{DstPath: "mnt", SrcFSID: "a", SrcPath: ".", Mode: "replace", Index: 1},
			{DstPath: "mnt", SrcFSID: "c", SrcPath: ".", Mode: "before", Index: 2},
		},
	}
	if !reflect.DeepEqual(roundTrip, want) {
		t.Fatalf("round trip manifest = %#v, want %#v", roundTrip, want)
	}
}

func TestImportManifestRootAndSystemBinds(t *testing.T) {
	root := newNamespaceImportFS("root")
	task := newNamespaceImportFS("task")
	filesystems := map[string]fs.FS{
		"root": root,
		"task": task,
	}

	manifest := migration.NamespaceManifest{
		TaskID: "task1",
		Binds: []migration.BindManifest{
			{DstPath: ".", SrcFSID: "root", SrcPath: ".", Mode: "replace", Index: 0, Root: true},
			{DstPath: "#task", SrcFSID: "task", SrcPath: ".", Mode: "replace", Index: 0, System: true},
		},
	}
	imported, err := ImportManifest(context.Background(), manifest, namespaceImportLookup(filesystems))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := imported.ExportManifest("task1", namespaceImportResolver)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]migration.BindManifest{}
	for _, bind := range roundTrip.Binds {
		seen[bind.DstPath] = bind
	}
	if !seen["."].Root || seen["."].System {
		t.Fatalf("root bind shape = %#v", seen["."])
	}
	if !seen["#task"].System || seen["#task"].Root {
		t.Fatalf("system bind shape = %#v", seen["#task"])
	}
}

func TestImportManifestRejectsInvalidInput(t *testing.T) {
	known := newNamespaceImportFS("known")
	validBind := migration.BindManifest{
		DstPath: ".",
		SrcFSID: "known",
		SrcPath: ".",
		Mode:    "replace",
		Index:   0,
	}
	tests := []struct {
		name string
		bind migration.BindManifest
		err  error
	}{
		{
			name: "unknown filesystem",
			bind: migration.BindManifest{DstPath: ".", SrcFSID: "missing", SrcPath: ".", Mode: "replace", Index: 0},
			err:  migration.ErrUnknownFilesystem,
		},
		{
			name: "bad mode",
			bind: migration.BindManifest{DstPath: ".", SrcFSID: "known", SrcPath: ".", Mode: "sideways", Index: 0},
			err:  fs.ErrInvalid,
		},
		{
			name: "empty mode",
			bind: migration.BindManifest{DstPath: ".", SrcFSID: "known", SrcPath: ".", Mode: "", Index: 0},
			err:  fs.ErrInvalid,
		},
		{
			name: "bad destination",
			bind: migration.BindManifest{DstPath: "../escape", SrcFSID: "known", SrcPath: ".", Mode: "replace", Index: 0},
			err:  fs.ErrNotExist,
		},
		{
			name: "bad source path",
			bind: migration.BindManifest{DstPath: ".", SrcFSID: "known", SrcPath: "../escape", Mode: "replace", Index: 0},
			err:  fs.ErrNotExist,
		},
		{
			name: "empty filesystem id",
			bind: migration.BindManifest{DstPath: ".", SrcFSID: "", SrcPath: ".", Mode: "replace", Index: 0},
			err:  migration.ErrUnknownFilesystem,
		},
		{
			name: "missing source path",
			bind: migration.BindManifest{DstPath: ".", SrcFSID: "known", SrcPath: "missing", Mode: "replace", Index: 0},
			err:  fs.ErrNotExist,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ImportManifest(context.Background(), migration.NamespaceManifest{Binds: []migration.BindManifest{tt.bind}}, namespaceImportLookup(map[string]fs.FS{"known": known}))
			if !errors.Is(err, tt.err) {
				t.Fatalf("ImportManifest error = %v, want %v", err, tt.err)
			}
		})
	}

	_, err := ImportManifest(context.Background(), migration.NamespaceManifest{Binds: []migration.BindManifest{
		validBind,
		{DstPath: ".", SrcFSID: "known", SrcPath: ".", Mode: "after", Index: 2},
	}}, namespaceImportLookup(map[string]fs.FS{"known": known}))
	if err == nil || !strings.Contains(err.Error(), "non-contiguous index") {
		t.Fatalf("ImportManifest non-contiguous error = %v", err)
	}

	_, err = ImportManifest(context.Background(), migration.NamespaceManifest{Binds: []migration.BindManifest{
		validBind,
		{DstPath: ".", SrcFSID: "known", SrcPath: ".", Mode: "after", Index: 0},
	}}, namespaceImportLookup(map[string]fs.FS{"known": known}))
	if err == nil || !strings.Contains(err.Error(), "duplicate index") {
		t.Fatalf("ImportManifest duplicate index error = %v", err)
	}

	_, err = ImportManifest(context.Background(), migration.NamespaceManifest{Binds: []migration.BindManifest{
		{DstPath: ".", SrcFSID: "known", SrcPath: ".", Mode: "replace", Index: -1},
	}}, namespaceImportLookup(map[string]fs.FS{"known": known}))
	if err == nil || !strings.Contains(err.Error(), "negative index") {
		t.Fatalf("ImportManifest negative index error = %v", err)
	}

	lookupErr := errors.New("lookup failed")
	_, err = ImportManifest(context.Background(), migration.NamespaceManifest{Binds: []migration.BindManifest{validBind}}, func(string) (fs.FS, error) {
		return nil, lookupErr
	})
	if !errors.Is(err, lookupErr) {
		t.Fatalf("ImportManifest lookup error = %v, want %v", err, lookupErr)
	}

	_, err = ImportManifest(context.Background(), migration.NamespaceManifest{Binds: []migration.BindManifest{validBind}}, func(string) (fs.FS, error) {
		return nil, nil
	})
	if !errors.Is(err, migration.ErrUnknownFilesystem) {
		t.Fatalf("ImportManifest nil filesystem error = %v, want ErrUnknownFilesystem", err)
	}

	_, err = ImportManifest(context.Background(), migration.NamespaceManifest{Binds: []migration.BindManifest{validBind}}, nil)
	if !errors.Is(err, migration.ErrUnknownFilesystem) {
		t.Fatalf("ImportManifest nil lookup error = %v, want ErrUnknownFilesystem", err)
	}
}

func TestSynthesizedDirectories(t *testing.T) {
	// Create test filesystem
	testFS := fstest.MapFS{
		"file.txt": {Data: []byte("content")},
	}

	// Bind a file in a deep path
	ns := New(context.Background())
	ns.Bind(testFS, "file.txt", "a/b/c/file.txt", ModeAfter)

	// Test that we can read parent directories
	tests := []struct {
		path     string
		expected []string // expected entry names
	}{
		{".", []string{"a"}},
		{"a", []string{"b"}},
		{"a/b", []string{"c"}},
		{"a/b/c", []string{"file.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			entries, err := fs.ReadDir(ns, tt.path)
			if err != nil {
				t.Fatalf("ReadDir(%q) error: %v", tt.path, err)
			}

			if len(entries) != len(tt.expected) {
				t.Errorf("ReadDir(%q) got %d entries, want %d", tt.path, len(entries), len(tt.expected))
			}

			// Check entry names
			var got []string
			for _, entry := range entries {
				got = append(got, entry.Name())
				// Verify directory status
				if tt.path != "a/b/c" && !entry.IsDir() {
					t.Errorf("Entry %q in %q should be a directory", entry.Name(), tt.path)
				}
			}

			// Sort both slices for comparison
			sort.Strings(got)
			sort.Strings(tt.expected)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ReadDir(%q) got entries %v, want %v", tt.path, got, tt.expected)
			}
		})
	}

	// Test that we can open synthesized directories
	for _, path := range []string{"a", "a/b", "a/b/c"} {
		t.Run("Open/"+path, func(t *testing.T) {
			f, err := ns.Open(path)
			if err != nil {
				t.Fatalf("Open(%q) error: %v", path, err)
			}
			defer f.Close()

			// Verify it's a directory
			info, err := f.Stat()
			if err != nil {
				t.Fatalf("Stat() error: %v", err)
			}
			if !info.IsDir() {
				t.Errorf("Expected %q to be a directory", path)
			}
		})
	}
	// Test directory binding with synthesized parents
	ns2 := New(context.Background())
	ns2.Bind(testFS, ".", "x/y/z", ModeAfter)

	// Verify parent directories are synthesized
	dirs := []string{".", "x", "x/y", "x/y/z"}
	for _, dir := range dirs {
		t.Run("DirBind/"+dir, func(t *testing.T) {
			entries, err := fs.ReadDir(ns2, dir)
			if err != nil {
				t.Fatalf("ReadDir(%q) error: %v", dir, err)
			}

			if len(entries) != 1 {
				t.Fatalf("ReadDir(%q) got %d entries, want 1", dir, len(entries))
			}

			entry := entries[0]
			if dir != "x/y/z" && !entry.IsDir() {
				t.Errorf("Entry in %q should be a directory", dir)
			}

			var expectedName string
			switch dir {
			case ".":
				expectedName = "x"
			case "x":
				expectedName = "y"
			case "x/y":
				expectedName = "z"
			case "x/y/z":
				expectedName = "file.txt"
			}

			if entry.Name() != expectedName {
				t.Errorf("ReadDir(%q) got entry name %q, want %q", dir, entry.Name(), expectedName)
			}
		})
	}
}

type namespaceImportFS struct {
	id    string
	files fskit.MapFS
}

func newNamespaceImportFS(id string) *namespaceImportFS {
	return &namespaceImportFS{
		id: id,
		files: fskit.MapFS{
			"file.txt": fskit.RawNode([]byte(id)),
		},
	}
}

func (f *namespaceImportFS) Open(name string) (fs.File, error) {
	return f.files.Open(name)
}

func namespaceImportResolver(fsys fs.FS) (string, error) {
	f, ok := fsys.(*namespaceImportFS)
	if !ok {
		return "", migration.ErrUnknownFilesystem
	}
	return f.id, nil
}

func namespaceImportLookup(filesystems map[string]fs.FS) FSIDLookup {
	return func(id string) (fs.FS, error) {
		fsys, ok := filesystems[id]
		if !ok {
			return nil, migration.ErrUnknownFilesystem
		}
		return fsys, nil
	}
}

type manifestFS struct {
	id    string
	files fskit.MapFS
}

func newManifestFS(id string) *manifestFS {
	return &manifestFS{
		id: id,
		files: fskit.MapFS{
			"file.txt": fskit.RawNode([]byte(id)),
		},
	}
}

func (f *manifestFS) Open(name string) (fs.File, error) {
	return f.files.Open(name)
}

func (f *manifestFS) ResolveFS(context.Context, string) (fs.FS, string, error) {
	return f, ".", nil
}

func manifestResolver(fsys fs.FS) (string, error) {
	f, ok := fsys.(*manifestFS)
	if !ok {
		return "", migration.ErrUnknownFilesystem
	}
	return f.id, nil
}

func TestMkdirOnLeaf(t *testing.T) {
	ns := New(context.Background())

	memfs := memfs.From(fskit.MapFS{
		"file": fskit.RawNode([]byte("content")),
	})

	middlefs := fskit.MapFS{
		"dir": memfs,
	}

	ns.Bind(middlefs, ".", "sub", ModeAfter)

	// first we'll use ResolveFS manually to get the memfs

	subfs, _, err := ns.ResolveFS(context.Background(), "sub/dir/file")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(subfs, memfs) {
		t.Fatalf("ResolveFS(sub/dir/file) is not memfs: %T", subfs)
	}

	err = fs.Mkdir(subfs, "newdir1", 0755)
	if err != nil {
		t.Fatal(err)
	}

	dir, err := fs.ReadDir(ns, "sub/dir")
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(dir))
	}

	// now we'll use Mkdir on the namespace directly

	err = fs.Mkdir(ns, "sub/dir/newdir2", 0755)
	if err != nil {
		t.Fatal(err)
	}

	dir, err = fs.ReadDir(ns, "sub/dir")
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(dir))
	}

	// now we'll use MkdirAll on the namespace directly

	err = fs.MkdirAll(ns, "sub/dir/newdir3/newdir4/newdir5", 0755)
	if err != nil {
		t.Fatal(err)
	}

	dir, err = fs.ReadDir(ns, "sub/dir/newdir3/newdir4")
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(dir))
	}
}

func TestWritableRootOverRootBind(t *testing.T) {
	mfs := fskit.MapFS{
		"a": fskit.RawNode([]byte("content1")),
		"b": fskit.RawNode([]byte("content2")),
	}

	emptyfs := memfs.New()

	ns := New(context.Background())
	if err := ns.Bind(mfs, ".", ".", ModeAfter); err != nil {
		t.Fatal(err)
	}
	if err := ns.Bind(emptyfs, ".", ".", ModeAfter); err != nil {
		t.Fatal(err)
	}

	e, err := fs.ReadDir(ns, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 2 {
		// a, b
		t.Fatalf("unexpected number of entries: %v", len(e))
	}

	if err := fs.WriteFile(ns, "c", []byte("content3"), 0644); err != nil {
		t.Fatal(err)
	}

	e, err = fs.ReadDir(ns, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 3 {
		// a, b, c
		t.Fatalf("unexpected number of entries: %v", len(e))
	}

	n, ok := emptyfs.Node("c")
	if !ok {
		t.Fatal("c not found in emptyfs")
	}
	if !bytes.Equal(n.Data(), []byte("content3")) {
		t.Fatalf("unexpected data: %s", string(n.Data()))
	}
}
