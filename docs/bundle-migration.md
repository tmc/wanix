# Bundle migration

Wanix can export a running system into a bundle and import that bundle into
another Wanix system. A bundle contains a manifest plus the filesystem archives
and checkpoint payloads needed by the manifest.

Bundle migration is intended for checkpoint-aware workloads. It preserves
filesystem material, bind graph metadata, restorable file descriptors, VM state,
guest filesystem material, and cooperative task checkpoints. Running tasks that
do not provide checkpoint state fail closed at export time instead of being
silently restarted.

## Namespace API

The primary migration interface should be a Wanix namespace, not a browser-only
object protocol. A system should expose a bundle device, such as `#bundle`, that
can be reached through the same namespace mechanisms as `#task`, `#term`, and
`#vm`.

A bundle device should provide files for:

* `manifest`: the task, filesystem, VM, descriptor, and checkpoint metadata.
* `archive/<id>`: tar archives for filesystem material named by the manifest.
* `state/<id>`: VM and task checkpoint payloads named by the manifest.
* `ctl`: export, import, restore, and cleanup control operations.

The exact file names can change, but the model should not: export and restore
are operations on a mounted Wanix namespace. Browser helpers, host exports,
workbenches, and native tools should all be able to use the same surface.

For example, exporting a bundle should amount to reading the manifest and the
payload files from the source namespace. Importing should stage the manifest and
payloads into a target namespace and then request restore through `ctl`. State
payloads should be staged in device-owned storage, not written to arbitrary
manifest paths and removed later.

## Browser helper

The browser handle can still expose convenience helpers:

```js
const filesystems = window.WanixBundleFilesystems([
    {id: "rootfs", source: "."},
]);

const bundle = await source.root.exportBundle(filesystems);
const result = await target.root.importBundle(bundle);
```

These helpers should be wrappers over the namespace API. `exportBundle` records:

* `manifest`: task, filesystem, VM, descriptor, and checkpoint metadata.
* `archives`: tar archives for requested filesystem material.
* `states`: VM and task checkpoint payloads referenced by the manifest.

`importBundle` materializes the archives and state payloads into the target
bundle device and asks that device to restore the manifest. It should not define
a second migration protocol in JavaScript.

## Hostexport and workbench

`hostexport` exports a Wanix namespace over 9p. It should be treated as a
transport for the same bundle namespace, not as a separate migration mechanism.
Serial-backed host exports are latency-sensitive, so large archives and state
payloads may need caching or explicit staging. That is a transport concern; it
should not change the bundle format.

The workbench should attach to a namespace root and discover normal Wanix
devices below it. A workbench connected to a VM guest, a remote import, or a
native host export should use the same `#task`, `#term`, `#vm`, and `#bundle`
conventions once those devices are mounted in its namespace. Workbench-specific
task or terminal path overrides are useful as an escape hatch, but they should
not become the migration model.

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

See [../examples/bundle-checkpoint.html](../examples/bundle-checkpoint.html) for
a complete browser example that exports a source system and imports it into a
target system.

Non-browser tasks can participate only if their task driver implements an
equivalent cooperative checkpoint contract. Otherwise they must fail closed.

## Unsupported running tasks

Wanix does not capture arbitrary worker heap, timer, or message queue state
without a checkpoint. If a running task has no VM state and no task checkpoint
state, bundle export returns an error like:

```text
exportBundle: running task 2 missing checkpoint state
```

This fail-closed behavior keeps bundles from looking restorable when task state
would be lost.
