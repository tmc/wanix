package api

import (
	"math"
	"time"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix/fs"
)

func (s *syscaller) chtimes(r rpc.Responder, c *rpc.Call) {
	var args []any
	c.Receive(&args)

	path, ok := args[0].(string)
	if !ok {
		r.Return(errInvalidArg("chtimes", "path"))
		return
	}

	// atime and mtime are in seconds (with fractional parts)
	atime, err := decodeUnixSeconds(args, 1)
	if err != nil {
		r.Return(err)
		return
	}
	mtime, err := decodeUnixSeconds(args, 2)
	if err != nil {
		r.Return(err)
		return
	}

	err = fs.Chtimes(s.task.NS(), path, atime, mtime)
	if err != nil {
		r.Return(err)
		return
	}
}

func decodeUnixSeconds(args []any, index int) (time.Time, error) {
	if len(args) <= index {
		return time.Time{}, errInvalidArg("chtimes", "time")
	}
	sec, err := numberArg(args[index])
	if err != nil {
		return time.Time{}, err
	}
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(frac*1e9)), nil
}

func numberArg(v any) (float64, error) {
	switch v := v.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	default:
		return 0, errInvalidArg("chtimes", "time")
	}
}
