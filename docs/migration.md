# Wanix Migration

Wanix migration is component based. A bundle describes the parts needed to
restore a Wanix runtime shape, and each part is restored by the layer that owns
it.

The supported boundary covers namespace and filesystem state, task manifests,
copy-on-write filesystem descriptors, namespace archives, v86 control state, and
descriptor-only VM resources. Opaque live resources fail closed.

## Components

A migration bundle uses `migration.BundleManifest`.

`migration.ValidateBundleManifest` validates the manifest header. The current
version is `migration.BundleManifestVersion`; supported modes are
`migration.ModeMigrate` and `migration.ModeFork`.

Supported pieces:

- `filesystem` descriptors identify already materialized filesystems supplied by
  the restore caller.
- `cowfs` descriptors rebuild a copy-on-write filesystem from base and overlay
  filesystem IDs and persisted whiteout state.
- `task` manifests restore task identity, command metadata, namespace binds, and
  restorable open file descriptors.
- descriptor-only VM manifests restore VM identity, kind, and alias through a
  caller-supplied `wanix.VMRestoreFunc`.
- v86 control messages save and restore v86 state between compatible Wanix v86
  workers.
- namespace archive RPCs export and import filesystem material as tar.

Unsupported pieces fail closed:

- VM manifests with `state_path` at the Go `vm.Device` descriptor layer.
- Worker manifests and live worker handles.
- Task filesystem exports.
- Open file descriptors that are not path backed and restorable.
- Unknown component kinds or missing filesystem IDs.

## Namespace Archives

The browser API exposes two namespace archive operations:

- `Archive(name=".")` exports a slash-cleaned namespace-rooted subtree as tar
  bytes.
- `ImportArchive(name=".", contents)` materializes tar bytes into a
  slash-cleaned namespace path.

Both reject raw parent traversal before cleaning. Import also rejects unsafe tar
paths, existing destination files, root file replacement, unsupported tar entry
types, unsafe symlink targets, and filesystems that cannot be written.

Archive RPCs carry filesystem material. They do not preserve namespace bind
graphs, task tables, open resources, or v86 state by themselves.

## Bundle Restore

Use `wanix.RestoreBundle` when the manifest names its component filesystems and
the caller can provide those filesystems by ID.

Use `wanix.ValidateBundleRestore` to check the same fail-closed restore boundary
without resolving filesystem sources.

Restore order:

1. Validate the manifest header and reject unsupported resource state.
2. Restore descriptor-only VMs through `BundleRestoreOptions.RestoreVM`.
3. Restore filesystem descriptors, including `cowfs` descriptors.
4. Restore task manifests with namespace binds against the restored filesystem
   lookup.
5. Restore restorable open file descriptors.

If task or filesystem import fails, restored VMs and tasks created during that
import are rolled back.

See `ExampleRestoreBundle` in `example_migration_test.go` for a runnable minimal
bundle restore.

## Browser API

The browser handle exposes task bundle manifests through RPC:

```js
const filesystems = [
  { id: "rootfs", kind: "memfs", source: "mnt" },
  { id: "taskfs", kind: "taskfs", source: "#task" },
  { id: "wanixfs", kind: "system", source: "#wanix" },
];

const manifest = await system.root.bundleManifest(filesystems);
const result = await system.root.restoreBundleManifest(manifest);
```

Every filesystem descriptor passed to `bundleManifest` needs a `source` path
that resolves to the root of a bound filesystem. Include the system task and
Wanix sources when exported namespaces bind them. `restoreBundleManifest`
imports task manifests, restores descriptor-only VM manifests through `#vm`, and
returns restored task and VM IDs.

## Boundary

This implementation supports a Wanix browser runtime within the filesystem and
descriptor boundary: browser command control, v86 save/restore between matching
Wanix v86 workers, namespace tar export, namespace tar import/materialization,
task restore, cowfs restore, and descriptor-only VM restore.

It is not a transparent checkpoint of every live runtime object. Worker state,
task exports, non-restorable descriptors, and VM descriptor manifests that point
at state payloads remain fail-closed until those resources have explicit restore
contracts.
