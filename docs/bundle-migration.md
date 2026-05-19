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

A JavaScript task can participate in migration by replying to the
`wanix-checkpoint` `save-state` message with bytes that describe the task state.
When the task is restored, Wanix passes the checkpoint bytes back through the
worker metadata.

```js
const encoder = new TextEncoder();
const decoder = new TextDecoder();
let checkpoint = {counter: 0};

globalThis.onmessage = event => {
    const message = event.data || {};
    if (message.worker) {
        if (message.worker.checkpoint_state) {
            checkpoint = JSON.parse(decoder.decode(message.worker.checkpoint_state));
        }
        globalThis.wanixWorker = message.worker;
        return;
    }
    if (message.type !== "wanix-checkpoint" || message.op !== "save-state") {
        return;
    }
    globalThis.postMessage({
        type: "wanix-checkpoint",
        op: "save-state",
        id: message.id,
        ok: true,
        state: encoder.encode(JSON.stringify(checkpoint)),
    });
};
```

See [../examples/bundle-checkpoint.html](../examples/bundle-checkpoint.html) for
a complete browser example that exports a source system and imports it into a
target system.

## Unsupported running tasks

Wanix does not capture arbitrary worker heap, timer, or message queue state
without a checkpoint. If a running task has no VM state and no task checkpoint
state, `exportBundle` returns an error like:

```text
exportBundle: running task 2 missing checkpoint state
```

This fail-closed behavior keeps bundles from looking restorable when task state
would be lost.
