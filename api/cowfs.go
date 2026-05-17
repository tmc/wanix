package api

import (
	"context"
	"fmt"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/cowfs"
	"tractor.dev/wanix/fs/vfs"
)

func (s *syscaller) bindCowFS(r rpc.Responder, c *rpc.Call) {
	var args []string
	c.Receive(&args)
	if len(args) < 3 {
		r.Return(errInvalidArg("bindCowFS", "arguments"))
		return
	}
	whiteout := ""
	if len(args) > 3 {
		whiteout = args[3]
	}
	ns := s.task.NS()
	base, err := cowFSLayer(s.task.Context(), ns, args[0])
	if err != nil {
		r.Return(fmt.Errorf("bind cowfs base %s: %w", args[0], err))
		return
	}
	overlay, err := cowFSLayer(s.task.Context(), ns, args[1])
	if err != nil {
		r.Return(fmt.Errorf("bind cowfs overlay %s: %w", args[1], err))
		return
	}
	fsys, err := cowfs.Restore(base, overlay, whiteout)
	if err != nil {
		r.Return(err)
		return
	}
	if err := ns.Bind(fsys, ".", args[2], vfs.ModeReplace); err != nil {
		r.Return(err)
		return
	}
}

func cowFSLayer(ctx context.Context, ns *vfs.NS, name string) (fs.FS, error) {
	fsys, sub, err := fs.Resolve(ns, ctx, name)
	if err != nil {
		return nil, err
	}
	if sub == "." {
		return fsys, nil
	}
	return fs.Sub(fsys, sub)
}
