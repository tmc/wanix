package tarfs

import (
	"archive/tar"
	"io"
	iofs "io/fs"

	"tractor.dev/wanix/fs"
)

// Archive writes fsys to tw as a tar archive.
func Archive(fsys iofs.FS, tw *tar.Writer) error {
	return ArchivePath(fsys, ".", tw)
}

// ArchivePath writes the subtree rooted at root to tw as a tar archive.
func ArchivePath(fsys iofs.FS, root string, tw *tar.Writer) error {
	if root == "" {
		root = "."
	}
	sub, err := fs.Sub(fsys, root)
	if err != nil {
		return err
	}
	return iofs.WalkDir(sub, ".", func(path string, entry iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		var link string
		if info.Mode()&iofs.ModeSymlink != 0 {
			link, err = fs.Readlink(sub, path)
			if err != nil {
				return err
			}
		}

		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = path
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			header.Size = 0
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		f, err := sub.Open(path)
		if err != nil {
			return err
		}

		_, err = io.Copy(tw, f)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		return err
	})
}
