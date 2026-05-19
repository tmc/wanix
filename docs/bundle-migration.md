# Bundle migration

Wanix can export a running system into a bundle and import that bundle into
another Wanix system. A bundle contains a manifest plus the filesystem archives
and checkpoint payloads needed by the manifest.

Bundle migration is intended for checkpoint-aware workloads. It preserves
filesystem material, bind graph metadata, restorable file descriptors, VM state,
guest filesystem material, service worker bindings, and cooperative task
checkpoints. Running tasks that do not provide checkpoint state fail closed at
export time instead of being silently restarted.

## Browser API

The browser handle exposes the bundle operations:

```js
const filesystems = window.WanixBundleFilesystems([
    {id: "rootfs", source: "."},
]);

const bundle = await source.root.exportBundle(filesystems);
const result = await target.root.importBundle(bundle);
```

`exportBundle` records:

* `manifest`: task, filesystem, VM, descriptor, and service worker metadata.
* `archives`: tar archives for requested filesystem material.
* `states`: VM and task checkpoint payloads referenced by the manifest.

`importBundle` materializes the archives and state payloads, restores the
manifest, and removes temporary state files after restore.

## Cooperative task checkpoints

A JavaScript task can participate in migration by installing the
`WanixCheckpoint` helper and returning bytes that describe the task state. The
checkpoint protocol is version 1 and uses `wanix-checkpoint` `save-state`
messages internally. When the task is restored, Wanix passes the checkpoint
bytes back through the helper's `load` callback.

```js
const encoder = new TextEncoder();
const decoder = new TextDecoder();
let checkpoint = {counter: 0};

globalThis.WanixCheckpoint.install({
    load(state) {
        checkpoint = JSON.parse(decoder.decode(state));
    },
    save() {
        return encoder.encode(JSON.stringify(checkpoint));
    },
});
```

Go WebAssembly programs should use the Go checkpoint helper instead of handling
worker messages directly:

```go
package main

import "tractor.dev/wanix/checkpoint"

func main() {
	var state []byte
	checkpoint.Register(checkpoint.Handler{
		Load: func(data []byte) error {
			state = append(state[:0], data...)
			return nil
		},
		Save: func() ([]byte, error) {
			return append([]byte(nil), state...), nil
		},
	})

	// Run the program.
}
```

The helper is cooperative. A program returns `checkpoint.ErrUnsupported` when it
is not at a checkpoint boundary, so bundle export fails closed instead of
recording partial runtime state.

See [../examples/bundle-checkpoint.html](../examples/bundle-checkpoint.html) for
a complete browser example that exports a source system and imports it into a
target system.

## rc checkpoints

The `rc` shell checkpoints only prompt-boundary state: current directory and
exported environment. It does not checkpoint command history, a foreground
command, pipeline, partial input line, terminal buffer, background job, or
arbitrary Go runtime state. While a command is running, `rc` reports
`checkpoint.ErrUnsupported`, causing bundle export to fail closed.

## Unsupported running tasks

Wanix does not capture arbitrary worker heap, timer, or message queue state
without a checkpoint. If a running task has no VM state and no task checkpoint
state, `exportBundle` returns an error like:

```text
exportBundle: running task 2 missing checkpoint state
```

This fail-closed behavior keeps bundles from looking restorable when task state
would be lost.
