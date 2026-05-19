//go:build js

package checkpoint

import (
	"fmt"
	"syscall/js"
)

const (
	stateFuncName = "wanixCheckpointState"
)

var saveFuncs []js.Func

// Register installs h as the process checkpoint handler.
func Register(h Handler) error {
	target := js.Global()
	if state := target.Get("checkpoint_state"); !state.IsUndefined() && !state.IsNull() && h.Load != nil {
		data, err := bytesFromJS(state)
		if err != nil {
			return fmt.Errorf("load checkpoint: %w", err)
		}
		if err := h.Load(data); err != nil {
			return fmt.Errorf("load checkpoint: %w", err)
		}
	}

	save := js.FuncOf(func(this js.Value, args []js.Value) any {
		return savePromise(h)
	})
	saveFuncs = append(saveFuncs, save)
	target.Set(stateFuncName, save)
	return nil
}

func savePromise(h Handler) js.Value {
	promise := js.Global().Get("Promise")
	executor := js.FuncOf(func(this js.Value, args []js.Value) any {
		resolve := args[0]
		reject := args[1]
		if h.Save == nil {
			reject.Invoke(jsError(ErrUnsupported))
			return nil
		}
		data, err := h.Save()
		if err != nil {
			reject.Invoke(jsError(err))
			return nil
		}
		buf := js.Global().Get("Uint8Array").New(len(data))
		js.CopyBytesToJS(buf, data)
		resolve.Invoke(buf)
		return nil
	})
	defer executor.Release()
	return promise.New(executor)
}

func jsError(err error) js.Value {
	return js.Global().Get("Error").New(err.Error())
}

func bytesFromJS(v js.Value) ([]byte, error) {
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
