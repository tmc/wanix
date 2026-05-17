package tarfs

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/memfs"
)

func TestArchivePathUsesRelativeNames(t *testing.T) {
	fsys := memfs.From(fskit.MapFS{
		"dir/file.txt": fskit.RawNode([]byte("hello")),
		"other.txt":    fskit.RawNode([]byte("outside")),
	})

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := ArchivePath(fsys, "dir", tw); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			got[hdr.Name] = string(data)
		}
	}
	if got["file.txt"] != "hello" {
		t.Fatalf("archive file.txt = %q, want hello; all files %#v", got["file.txt"], got)
	}
	if _, ok := got["dir/file.txt"]; ok {
		t.Fatalf("archive used rooted name dir/file.txt: %#v", got)
	}
	if _, ok := got["other.txt"]; ok {
		t.Fatalf("archive included sibling outside root: %#v", got)
	}
}

func TestArchiveUsesFilesystemReadlink(t *testing.T) {
	fsys := memfs.From(fskit.MapFS{
		"target.txt": fskit.RawNode([]byte("target")),
	})
	fsys.SetNode("link.txt", fskit.RawNode([]byte("target.txt"), fs.ModeSymlink|0777))

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := Archive(fsys, tw); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name != "link.txt" {
			continue
		}
		if hdr.Typeflag != tar.TypeSymlink {
			t.Fatalf("link type = %v, want symlink", hdr.Typeflag)
		}
		if hdr.Linkname != "target.txt" {
			t.Fatalf("link target = %q, want target.txt", hdr.Linkname)
		}
		return
	}
	t.Fatal("archive did not include link.txt")
}
