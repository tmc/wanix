package api

import (
	"context"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix"
	"tractor.dev/wanix/migration"
)

func (s *syscaller) bundleVMStates(r rpc.Responder, c *rpc.Call) {
	var args []any
	c.Receive(&args)

	states, err := collectBundleVMStates(s.task.Root(), s.task.Context())
	if err != nil {
		r.Return(err)
		return
	}
	r.Return(states)
}

func (s *syscaller) bundleTaskStates(r rpc.Responder, c *rpc.Call) {
	var args []any
	c.Receive(&args)

	states, err := collectBundleTaskStates(s.task.Root(), s.task.Context())
	if err != nil {
		r.Return(err)
		return
	}
	r.Return(states)
}

func taskVMID(t *wanix.Task) string {
	for _, line := range t.Env() {
		key, value, ok := cutEnv(line)
		if ok && key == "vm" {
			return value
		}
	}
	return ""
}

func vmIDLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func cutEnv(line string) (key, value string, ok bool) {
	for i := 0; i < len(line); i++ {
		if line[i] == '=' {
			return line[:i], line[i+1:], true
		}
	}
	return "", "", false
}

func collectBundleVMStates(root *wanix.Task, ctx context.Context) ([]migration.VMStatePayload, error) {
	return collectBundleVMStatesPlatform(root, ctx)
}

func collectBundleTaskStates(root *wanix.Task, ctx context.Context) ([]migration.TaskStatePayload, error) {
	return collectBundleTaskStatesPlatform(root, ctx)
}
