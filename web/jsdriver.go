//go:build js && wasm

package web

import (
	"strings"
	"syscall/js"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/migration"
	"tractor.dev/wanix/web/worker"
)

// this was added for demos and completeness, but is really just a sketch atm.
type JSDriver struct {
	Workers *worker.Device
	Root    *wanix.Task
}

func (d *JSDriver) Check(t *wanix.Task) bool {
	return strings.HasSuffix(t.Arg(0), ".js")
}

func (d *JSDriver) Start(t *wanix.Task) error {
	return d.start(t, nil)
}

func (d *JSDriver) start(t *wanix.Task, state []byte) error {
	data, err := fs.ReadFile(d.Root.NS(), t.Arg(0))
	if err != nil {
		return err
	}
	src := append([]byte(jsCheckpointRuntime), data...)
	jsBuf := js.Global().Get("Uint8Array").New(len(src))
	js.CopyBytesToJS(jsBuf, src)
	blob := js.Global().Get("Blob").New([]any{jsBuf}, js.ValueOf(map[string]any{"type": "text/javascript"}))
	url := js.Global().Get("URL").Call("createObjectURL", blob)
	return worker.StartTaskWorkerWithState(d.Workers, t, url.String(), state)
}

// jsCheckpointRuntime mirrors api/checkpoint.js for generic JS task blobs, which
// cannot import the page bundle directly.
const jsCheckpointRuntime = `
globalThis.WanixCheckpoint = globalThis.WanixCheckpoint || (() => {
	const protocol = Object.freeze({version: 1, type: "wanix-checkpoint", saveStateOp: "save-state"});
	function isSaveStateMessage(message) {
		return !!message &&
			message.type === protocol.type &&
			message.op === protocol.saveStateOp &&
			(message.version === undefined || message.version === protocol.version);
	}
	function response(message, fields) {
		return Object.assign({
			type: protocol.type,
			op: protocol.saveStateOp,
			version: protocol.version,
			id: message && message.id || "",
		}, fields);
	}
	async function saveStateResponse(message, save) {
		if (typeof save !== "function") {
			return response(message, {ok: false, error: "migration unsupported"});
		}
		try {
			return response(message, {ok: true, state: await save(message)});
		} catch (error) {
			return response(message, {ok: false, error: String(error && error.message || error)});
		}
	}
	async function postSaveState(message, options = {}) {
		const target = options.target || globalThis;
		const save = options.save || globalThis.wanixCheckpointState;
		target.postMessage(await saveStateResponse(message, save));
	}
	function stateFromWorker(worker) {
		return worker && worker.checkpoint_state || null;
	}
	function install(options = {}) {
		const target = options.target || globalThis;
		const load = options.load;
		target.addEventListener("message", async event => {
			const message = event.data || {};
			if (message.worker) {
				globalThis.wanixWorker = message.worker;
				const state = stateFromWorker(message.worker);
				if (state && typeof load === "function") {
					await load(state, message.worker);
				}
				return;
			}
			if (!isSaveStateMessage(message)) {
				return;
			}
			await postSaveState(message, {
				target,
				save: options.save || globalThis.wanixCheckpointState,
			});
		});
	}
	return {protocol, isSaveStateMessage, response, saveStateResponse, postSaveState, stateFromWorker, install};
})();
`

func (d *JSDriver) RestoreTask(t *wanix.Task, manifest migration.TaskManifest) error {
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
