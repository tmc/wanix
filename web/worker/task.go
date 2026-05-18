//go:build js && wasm

package worker

import (
	"strings"
	"syscall/js"

	"tractor.dev/wanix"
)

func FromTask(t *wanix.Task) js.Value {
	w := wanix.GetWorker(t)
	if w == nil {
		return js.Undefined()
	}
	return w.(js.Value)
}

func StartTaskWorker(svc *Device, t *wanix.Task, blobURL string) error {
	return StartTaskWorkerWithState(svc, t, blobURL, nil)
}

func StartTaskWorkerWithState(svc *Device, t *wanix.Task, blobURL string, state []byte) error {
	w, err := svc.Alloc(t)
	if err != nil {
		return err
	}
	args := append([]string{blobURL}, strings.Split(t.Cmd(), " ")...)
	return w.StartWithState(state, args...)
}
