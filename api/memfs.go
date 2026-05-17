package api

import (
	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/vfs"
)

func (s *syscaller) bindMemFS(r rpc.Responder, c *rpc.Call) {
	var args []string
	c.Receive(&args)
	if len(args) < 1 {
		r.Return(errInvalidArg("bindMemFS", "arguments"))
		return
	}
	if err := s.task.NS().Bind(memfs.New(), ".", args[0], vfs.ModeReplace); err != nil {
		r.Return(err)
		return
	}
}
