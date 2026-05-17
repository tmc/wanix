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

		return archiveEntry(sub, path, info, tw)
	})
}

func archiveEntry(fsys iofs.FS, path string, info iofs.FileInfo, tw *tar.Writer) error {
	var link string
	if info.Mode()&iofs.ModeSymlink != 0 {
		target, err := fs.Readlink(fsys, path)
		if err != nil {
			return err
		}
		link = target
	}

	var data []byte
	if info.Mode().IsRegular() {
		f, err := fsys.Open(path)
		if err != nil {
			return err
		}
		data, err = io.ReadAll(f)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}

	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	header.Name = path
	if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
		header.Size = int64(len(data))
	} else {
		header.Size = 0
	}

	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	_, err = tw.Write(data)
	return err
}
