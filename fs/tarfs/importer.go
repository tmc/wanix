package tarfs

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"tractor.dev/wanix/fs"
)

// ImportResult summarizes files materialized from a tar archive.
type ImportResult struct {
	Entries  int   `json:"entries"`
	Dirs     int   `json:"dirs"`
	Files    int   `json:"files"`
	Symlinks int   `json:"symlinks"`
	Bytes    int64 `json:"bytes"`
}

// Import materializes the tar archive read from tr into fsys.
func Import(fsys fs.FS, tr *tar.Reader) (ImportResult, error) {
	return ImportPath(fsys, ".", tr)
}

// ImportPath materializes the tar archive read from tr under root in fsys.
func ImportPath(fsys fs.FS, root string, tr *tar.Reader) (ImportResult, error) {
	if root == "" {
		root = "."
	}
	if !fs.ValidPath(root) {
		return ImportResult{}, &fs.PathError{Op: "import", Path: root, Err: fs.ErrInvalid}
	}

	var result ImportResult
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return result, nil
		}
		if err != nil {
			return result, err
		}

		name, err := cleanTarPath(hdr.Name)
		if err != nil {
			return result, err
		}
		if name == "." && hdr.Typeflag != tar.TypeDir {
			return result, &fs.PathError{Op: "import", Path: hdr.Name, Err: fs.ErrInvalid}
		}
		dst := name
		if root != "." {
			dst = path.Join(root, name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirAll(fsys, dst, fs.FileMode(hdr.FileInfo().Mode().Perm())); err != nil {
				return result, err
			}
			result.Dirs++
		case tar.TypeReg, tar.TypeRegA:
			n, err := importRegular(fsys, dst, hdr, tr)
			if err != nil {
				return result, err
			}
			result.Files++
			result.Bytes += n
		case tar.TypeSymlink:
			if err := importSymlink(fsys, dst, hdr); err != nil {
				return result, err
			}
			result.Symlinks++
		default:
			return result, fmt.Errorf("import %s: unsupported tar entry type %q", hdr.Name, hdr.Typeflag)
		}
		result.Entries++
	}
}

func cleanTarPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", &fs.PathError{Op: "import", Path: name, Err: fs.ErrInvalid}
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") {
		return "", &fs.PathError{Op: "import", Path: name, Err: fs.ErrInvalid}
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." {
			return "", &fs.PathError{Op: "import", Path: name, Err: fs.ErrInvalid}
		}
	}
	name = path.Clean(name)
	if name == "." {
		return ".", nil
	}
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: "import", Path: name, Err: fs.ErrInvalid}
	}
	return name, nil
}

func mkdirAll(fsys fs.FS, name string, perm fs.FileMode) error {
	if name == "." {
		return nil
	}
	if perm == 0 {
		perm = 0o755
	}
	return fs.MkdirAll(fsys, name, perm)
}

func importRegular(fsys fs.FS, name string, hdr *tar.Header, r io.Reader) (int64, error) {
	if err := mkdirAll(fsys, path.Dir(name), 0o755); err != nil {
		return 0, err
	}
	if _, err := fs.Lstat(fsys, name); err == nil {
		return 0, &fs.PathError{Op: "import", Path: name, Err: fs.ErrExist}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	perm := fs.FileMode(hdr.FileInfo().Mode().Perm())
	if perm == 0 {
		perm = 0o644
	}
	f, err := fs.OpenFile(fsys, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, err
	}
	w, ok := f.(io.Writer)
	if !ok {
		_ = f.Close()
		return 0, fmt.Errorf("import %s: %w", hdr.Name, fs.ErrNotSupported)
	}
	n, err := io.Copy(w, r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return n, err
	}
	if n != hdr.Size {
		return n, fmt.Errorf("import %s: copied %d bytes, want %d", hdr.Name, n, hdr.Size)
	}
	if err := fs.Chmod(fsys, name, fs.FileMode(hdr.FileInfo().Mode())); err != nil {
		return n, err
	}
	if !hdr.ModTime.IsZero() {
		if err := fs.Chtimes(fsys, name, hdr.AccessTime, hdr.ModTime); err != nil {
			return n, err
		}
	}
	return n, nil
}

func importSymlink(fsys fs.FS, name string, hdr *tar.Header) error {
	target := strings.ReplaceAll(strings.TrimSpace(hdr.Linkname), "\\", "/")
	if target == "" || path.IsAbs(target) {
		return &fs.PathError{Op: "import", Path: hdr.Name, Err: fs.ErrInvalid}
	}
	for _, elem := range strings.Split(target, "/") {
		if elem == ".." {
			return &fs.PathError{Op: "import", Path: hdr.Name, Err: fs.ErrInvalid}
		}
	}
	if err := mkdirAll(fsys, path.Dir(name), 0o755); err != nil {
		return err
	}
	return fs.Symlink(fsys, target, name)
}
