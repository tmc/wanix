package api

import (
	"archive/tar"
	"bytes"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix/fs/tarfs"
)

func (s *syscaller) importArchive(r rpc.Responder, c *rpc.Call) {
	var args []any
	c.Receive(&args)

	name := "."
	if len(args) > 0 {
		raw, ok := args[0].(string)
		if !ok {
			r.Return(errInvalidArg("importArchive", "path"))
			return
		}
		var err error
		name, err = cleanArchivePath(raw)
		if err != nil {
			r.Return(err)
			return
		}
	}

	if len(args) < 2 {
		r.Return(errInvalidArg("importArchive", "archive"))
		return
	}
	data, ok := args[1].([]byte)
	if !ok {
		r.Return(errInvalidArg("importArchive", "archive"))
		return
	}

	result, err := tarfs.ImportPath(s.task.NS(), name, tar.NewReader(bytes.NewReader(data)))
	if err != nil {
		r.Return(err)
		return
	}
	r.Return(result)
}

func errInvalidArg(op, arg string) error {
	return &typeError{op: op, arg: arg}
}

type typeError struct {
	op  string
	arg string
}

func (e *typeError) Error() string {
	return e.op + ": invalid " + e.arg
}
