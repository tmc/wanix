//go:build js

package api

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"syscall/js"
	"time"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/migration"
	"tractor.dev/wanix/vm"
)

func collectBundleVMStatesPlatform(root *wanix.Task, ctx context.Context) ([]bundleVMState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var out []bundleVMState
	seen := make(map[string]bool)
	for _, task := range root.Tasks() {
		vmID := taskVMID(task)
		if vmID == "" || seen[vmID] {
			continue
		}
		seen[vmID] = true
		v, err := lookupVM(root, ctx, vmID)
		if err != nil {
			return nil, err
		}
		state := bundleVMState{
			ID:   v.ID(),
			Kind: v.Kind(),
		}
		if alias := v.Alias(); alias != "" {
			state.Labels = map[string]string{"alias": alias}
		}
		if wanix.GetWorker(task) != nil {
			data, err := saveWorkerState(ctx, task, vmID)
			if err != nil {
				return nil, fmt.Errorf("bundle vm %s state: %w", vmID, err)
			}
			state.StatePath = vmStatePath(vmID)
			state.Data = data
		}
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool {
		return vmIDLess(out[i].ID, out[j].ID)
	})
	return out, nil
}

func collectBundleTaskStatesPlatform(root *wanix.Task, ctx context.Context) ([]bundleTaskState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var out []bundleTaskState
	for _, task := range root.Tasks() {
		if taskVMID(task) != "" || wanix.GetWorker(task) == nil {
			continue
		}
		saveCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		data, err := saveTaskState(saveCtx, task)
		cancel()
		if err != nil {
			if errors.Is(err, migration.ErrUnsupported) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				continue
			}
			return nil, fmt.Errorf("bundle task %s state: %w", task.ID(), err)
		}
		out = append(out, bundleTaskState{
			ID:        task.ID(),
			StatePath: taskStatePath(task.ID()),
			Data:      data,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return vmIDLess(out[i].ID, out[j].ID)
	})
	return out, nil
}

func lookupVM(root *wanix.Task, ctx context.Context, id string) (*vm.VM, error) {
	rfsys, _, err := fs.Resolve(root.NS(), ctx, path.Join("#vm", id))
	if err != nil {
		return nil, fmt.Errorf("bundle vm %s: %w", id, err)
	}
	vms, ok := rfsys.(*vm.Device)
	if !ok {
		return nil, fmt.Errorf("bundle vm %s: %w", id, fs.ErrInvalid)
	}
	v, err := vms.Lookup(id)
	if err != nil {
		return nil, fmt.Errorf("bundle vm %s: %w", id, err)
	}
	return v, nil
}

func saveWorkerState(ctx context.Context, task *wanix.Task, id string) ([]byte, error) {
	worker, ok := wanix.GetWorker(task).(js.Value)
	if !ok || worker.IsUndefined() || worker.IsNull() {
		return nil, migration.ErrUnsupported
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	listener := js.FuncOf(func(this js.Value, args []js.Value) any {
		msg := args[0].Get("data")
		if msg.Get("type").String() != "v86-control" || msg.Get("op").String() != "save-state" {
			return nil
		}
		if got := msg.Get("id"); got.Type() == js.TypeString && got.String() != id {
			return nil
		}
		if ok := msg.Get("ok"); ok.Type() == js.TypeBoolean && !ok.Bool() {
			errText := migration.ErrUnsupported.Error()
			if field := msg.Get("error"); field.Type() == js.TypeString && field.String() != "" {
				errText = field.String()
			}
			select {
			case done <- result{err: fmt.Errorf("%s", errText)}:
			default:
			}
			return nil
		}
		data, err := jsBytes(msg.Get("state"))
		select {
		case done <- result{data: data, err: err}:
		default:
		}
		return nil
	})
	worker.Call("addEventListener", "message", listener)
	defer listener.Release()
	defer worker.Call("removeEventListener", "message", listener)

	worker.Call("postMessage", map[string]any{"type": "save-state", "id": id})
	select {
	case res := <-done:
		return res.data, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func saveTaskState(ctx context.Context, task *wanix.Task) ([]byte, error) {
	worker, ok := wanix.GetWorker(task).(js.Value)
	if !ok || worker.IsUndefined() || worker.IsNull() {
		return nil, migration.ErrUnsupported
	}
	id := task.ID()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	listener := js.FuncOf(func(this js.Value, args []js.Value) any {
		msg := args[0].Get("data")
		if msg.Get("type").String() != "wanix-checkpoint" || msg.Get("op").String() != "save-state" {
			return nil
		}
		if got := msg.Get("id"); got.Type() == js.TypeString && got.String() != id {
			return nil
		}
		if ok := msg.Get("ok"); ok.Type() == js.TypeBoolean && !ok.Bool() {
			errText := migration.ErrUnsupported.Error()
			if field := msg.Get("error"); field.Type() == js.TypeString && field.String() != "" {
				errText = field.String()
			}
			select {
			case done <- result{err: fmt.Errorf("%s: %w", errText, migration.ErrUnsupported)}:
			default:
			}
			return nil
		}
		data, err := jsBytes(msg.Get("state"))
		select {
		case done <- result{data: data, err: err}:
		default:
		}
		return nil
	})
	worker.Call("addEventListener", "message", listener)
	defer listener.Release()
	defer worker.Call("removeEventListener", "message", listener)

	worker.Call("postMessage", map[string]any{
		"type": "wanix-checkpoint",
		"op":   "save-state",
		"id":   id,
	})
	select {
	case res := <-done:
		return res.data, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func jsBytes(v js.Value) ([]byte, error) {
	if v.IsUndefined() || v.IsNull() {
		return nil, fmt.Errorf("missing state")
	}
	if v.Type() == js.TypeString {
		return []byte(v.String()), nil
	}
	var view js.Value
	if buffer := v.Get("buffer"); !buffer.IsUndefined() && !buffer.IsNull() {
		offset := 0
		if byteOffset := v.Get("byteOffset"); byteOffset.Type() == js.TypeNumber {
			offset = byteOffset.Int()
		}
		length := v.Get("byteLength")
		if length.Type() != js.TypeNumber {
			return nil, fmt.Errorf("state must include byteLength")
		}
		view = js.Global().Get("Uint8Array").New(buffer, offset, length.Int())
	} else if v.Get("byteLength").Type() == js.TypeNumber {
		view = js.Global().Get("Uint8Array").New(v)
	} else {
		return nil, fmt.Errorf("state must be ArrayBuffer or typed array")
	}
	data := make([]byte, view.Get("byteLength").Int())
	js.CopyBytesToGo(data, view)
	return data, nil
}
