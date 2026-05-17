package tarfs

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	iofs "io/fs"
	"testing"
	"time"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/memfs"
)

func TestImportPathMaterializesArchive(t *testing.T) {
	src := memfs.From(fskit.MapFS{
		"dir/file.txt": fskit.RawNode([]byte("hello"), fs.FileMode(0o640)),
	})
	src.SetNode("dir/link.txt", fskit.RawNode("link.txt", []byte("file.txt"), fs.ModeSymlink|0o777))

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := ArchivePath(src, "dir", tw); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst := memfs.New()
	got, err := ImportPath(dst, "restored", tar.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if got.Files != 1 || got.Symlinks != 1 || got.Bytes != 5 {
		t.Fatalf("ImportPath result = %#v", got)
	}
	data, err := fs.ReadFile(dst, "restored/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("restored file = %q, want hello", data)
	}
	target, err := fs.Readlink(dst, "restored/link.txt")
	if err != nil {
		t.Fatal(err)
	}
	if target != "file.txt" {
		t.Fatalf("restored link = %q, want file.txt", target)
	}
}

func TestImportRejectsUnsafePaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"parent", "../evil.txt"},
		{"nested parent", "dir/../evil.txt"},
		{"absolute", "/evil.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{
				Name:    tt.path,
				Mode:    0o644,
				Size:    4,
				ModTime: time.Unix(1, 0),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(tw, "evil"); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}

			dst := memfs.New()
			if _, err := Import(dst, tar.NewReader(&buf)); err == nil {
				t.Fatal("Import error = nil, want error")
			}
			if _, err := fs.Stat(dst, "evil.txt"); !errors.Is(err, iofs.ErrNotExist) {
				t.Fatalf("evil.txt stat error = %v, want not exist", err)
			}
		})
	}
}

func TestImportRejectsUnsupportedType(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst := memfs.New()
	if _, err := Import(dst, tar.NewReader(&buf)); err == nil {
		t.Fatal("Import error = nil, want unsupported type error")
	}
}

func TestImportRejectsRootFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: ".", Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, "x"); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst := memfs.New()
	if _, err := Import(dst, tar.NewReader(&buf)); err == nil {
		t.Fatal("Import error = nil, want root-file error")
	}
}

func TestImportRejectsUnsafeSymlinkTarget(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "link.txt",
		Linkname: "../target.txt",
		Typeflag: tar.TypeSymlink,
		Mode:     0o777,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst := memfs.New()
	if _, err := Import(dst, tar.NewReader(&buf)); err == nil {
		t.Fatal("Import error = nil, want symlink target error")
	}
	if _, err := fs.Lstat(dst, "link.txt"); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("link.txt lstat error = %v, want not exist", err)
	}
}

func TestImportRejectsExistingFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "file.txt", Mode: 0o644, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, "new"); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst := memfs.From(fskit.MapFS{
		"file.txt": fskit.RawNode([]byte("old")),
	})
	if _, err := Import(dst, tar.NewReader(&buf)); err == nil {
		t.Fatal("Import error = nil, want existing-file error")
	}
	data, err := fs.ReadFile(dst, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("existing file = %q, want old", data)
	}
}
