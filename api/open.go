package api

import (
	"os"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix/fs"
)

func (s *syscaller) open(r rpc.Responder, c *rpc.Call) {
	var args []string
	c.Receive(&args)

	f, err := s.task.NS().Open(args[0])
	if err != nil {
		r.Return(err)
		return
	}

	fd := s.task.OpenFDWithFlags(f, args[0], os.O_RDONLY)
	r.Return(uint64(fd))
}

func (s *syscaller) create(r rpc.Responder, c *rpc.Call) {
	var args []string
	c.Receive(&args)

	f, err := fs.Create(s.task.NS(), args[0])
	if err != nil {
		r.Return(err)
		return
	}

	fd := s.task.OpenFDWithFlags(f, args[0], os.O_RDWR|os.O_CREATE|os.O_TRUNC)
	r.Return(uint64(fd))
}

func (s *syscaller) openFile(r rpc.Responder, c *rpc.Call) {
	var args []any
	c.Receive(&args)

	path, ok := args[0].(string)
	if !ok {
		panic("arg 0 is not a string")
	}

	flags, ok := args[1].(uint64)
	if !ok {
		panic("arg 1 is not a uint64")
	}

	mode, ok := args[2].(uint64)
	if !ok {
		panic("arg 2 is not a uint64")
	}

	f, err := fs.OpenFile(s.task.NS(), path, int(flags), fs.FileMode(mode))
	if err != nil {
		r.Return(err)
		return
	}

	fd := s.task.OpenFDWithFlags(f, path, int(flags))
	r.Return(uint64(fd))
}
