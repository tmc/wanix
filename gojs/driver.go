//go:build js && wasm

package gojs

import (
	"strings"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	gojsworker "tractor.dev/wanix/gojs/worker"
	"tractor.dev/wanix/migration"
	"tractor.dev/wanix/web/worker"
)

type Driver struct {
	Workers *worker.Device
}

func (d *Driver) Check(t *wanix.Task) bool {
	// todo: gojs detection
	return strings.HasSuffix(t.Arg(0), ".wasm")
}

func (d *Driver) Start(t *wanix.Task) error {
	return d.start(t, nil)
}

func (d *Driver) start(t *wanix.Task, state []byte) error {
	return worker.StartTaskWorkerWithState(d.Workers, t, gojsworker.BlobURL(), state)
}

func (d *Driver) RestoreTask(t *wanix.Task, manifest migration.TaskManifest) error {
	var state []byte
	if manifest.StatePath != "" {
		var err error
		state, err = fs.ReadFile(t.NS(), manifest.StatePath)
		if err != nil {
			return err
		}
	}
	return d.start(t, state)
}
