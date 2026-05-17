package api

import (
	"archive/tar"
	"bytes"
	"path"
	"strings"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/tarfs"
)

func (s *syscaller) archive(r rpc.Responder, c *rpc.Call) {
	var args []string
	c.Receive(&args)

	name := "."
	if len(args) > 0 {
		var err error
		name, err = cleanArchivePath(args[0])
		if err != nil {
			r.Return(err)
			return
		}
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := tarfs.ArchivePath(s.task.NS(), name, tw)
	if closeErr := tw.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		r.Return(err)
		return
	}
	r.Return(buf.Bytes())
}

func cleanArchivePath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ".", nil
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." {
			return "", &fs.PathError{Op: "archive", Path: name, Err: fs.ErrInvalid}
		}
	}
	name = strings.TrimLeft(name, "/")
	if name == "" {
		return ".", nil
	}
	name = path.Clean(name)
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: "archive", Path: name, Err: fs.ErrInvalid}
	}
	return name, nil
}
