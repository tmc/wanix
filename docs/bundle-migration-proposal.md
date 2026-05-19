# Proposal: Wanix Bundles and Checkpointable State

Wanix should support moving checkpoint-aware systems between Wanix runtimes by
exporting a bundle from one system and importing it into another. A bundle is a
manifest plus payloads: filesystem archives, VM state, task checkpoint state,
and the metadata needed to reconnect them.

This is not transparent migration of arbitrary browser workers. It is a
cooperative checkpoint model. Workloads that can describe their own runtime
state can move. Workloads that cannot do so fail closed instead of being
silently restarted with missing state.

## Prior Art

The closest prior art is VM snapshot and migration systems, such as
QEMU/libvirt-style machine state, and process checkpoint/restore systems such
as CRIU. OCI also uses the word "bundle" for metadata plus a root filesystem.

Wanix bundles use the same broad shape: metadata plus filesystem material plus
runtime state. They are not byte-compatible with those systems. The Wanix
runtime is browser- and Wasm-based, so the contract is explicit manifest data
and explicit state payloads rather than kernel- or hypervisor-owned hidden
state.

## Goals

* Export and import filesystem material.
* Preserve namespace and bind graph metadata.
* Preserve restorable file descriptors.
* Preserve VM state as serialized state payloads.
* Preserve cooperative task checkpoints.
* Reject unsupported running state before producing a misleading bundle.
* Keep the manifest format small enough to review and validate.

## Non-Goals

* Capturing arbitrary JavaScript worker heap state.
* Capturing browser timer queues or pending message queues.
* Treating every non-seekable descriptor as restorable.
* Hiding runtime loss behind automatic restart behavior.

Those may become separate runtime projects later. They should not be implied by
this proposal.

## Bundle Model

A bundle contains:

* `wanix-migration.json`, a versioned manifest.
* Filesystem archives, usually tar payloads.
* VM state blobs referenced by VM manifest entries.
* Task checkpoint blobs referenced by task manifest entries.

The manifest describes how to reconnect those payloads:

* filesystem descriptors name archives, bind sources, cowfs overlays, and guest
  filesystem material;
* namespace descriptors record the bind graph;
* task descriptors record task metadata, file descriptors, and checkpoint state;
* VM descriptors record VM kind, labels, guest filesystem ID, and state path;
* worker descriptors record checkpoint-aware worker state.

The import path validates the manifest before materializing it. Invalid paths,
duplicate IDs, missing filesystem IDs, and unsupported descriptor kinds fail
before restore proceeds.

## VM State as a Primitive

VM state should be treated as a reusable primitive, not as a bundle-only idea.
The bundle path is the first complete consumer because it needs to reconnect VM
state with filesystem and task state, but the lower-level model is simpler:

* a VM resource can expose serialized machine state;
* a VM manifest can name a state payload;
* import can attach that state to a VM resource before the VM resumes.

This keeps bundle migration from becoming the only API shape. A future direct
VM snapshot operation should use the same state carrier without requiring a
full system bundle.

The direct VM API should be exposed through the VM resource itself, for example
as `save-state` and `restore-state` operations on the VM `ctl` file. Bundles
would then be one caller of the VM state primitive, not the owner of it.

## Cooperative Task Checkpoints

Tasks that can preserve their own runtime state install the `WanixCheckpoint`
helper. The current protocol is version 1. Export asks the task to produce
state bytes. Import passes those bytes back when the task is restored.

This works for tasks that opt into the contract. It does not claim to serialize
the browser's private worker state.

Running tasks without checkpoint state fail closed. That behavior is part of the
contract: a successful bundle should mean the exported runtime state was
represented, not guessed.

## File Descriptor State

File descriptor manifests are for descriptor state that can be serialized and
restored safely. Path-backed seekable files and directory descriptors are the
initial supported cases.

Non-seekable streams are not automatically restorable. In particular, Wanix
`fs/pipe` is a port-backed stream endpoint, not a POSIX named pipe. It should
not be restored as a FIFO descriptor unless a later proposal defines a real
pipe checkpoint model.

## Filesystem State

Filesystem archives carry materialized file contents. Filesystem descriptors
carry the graph needed to reattach those archives under the right namespace.
The browser API packages this material through archive and import-archive
operations so an export can carry more than one filesystem payload.

Copy-on-write filesystems need more than a flat copy. Their descriptors preserve
base and overlay identities, plus whiteout metadata, so import can preserve the
overlay identity and lifecycle instead of collapsing it into an ordinary
directory.

## Failure Model

Export and import should prefer explicit errors over best-effort behavior:

* missing task checkpoint state fails export;
* unknown filesystem IDs fail import;
* invalid paths fail validation;
* unsupported file descriptor kinds fail restore;
* VM manifests that name state require a corresponding state payload.

This makes the system predictable for reviewers and for applications using the
bundle API.

## Review Plan

The current implementation should be reviewed as a stack:

1. archive and namespace materialization;
2. migration manifest schema and validation;
3. task, namespace, descriptor, and filesystem restore;
4. VM and v86 state paths;
5. browser bundle API and harness proof;
6. cooperative worker checkpoints and fail-closed running tasks;
7. docs and examples;
8. shared checkpoint helper and protocol versioning.

That order keeps the reusable primitives visible. Bundle migration is the
integration point, not the whole design.

The browser proof should exercise archive export, archive import, manifest
restore, VM state restore, cooperative task checkpoint restore, and fail-closed
unsupported running-task behavior as separate observable cases.

## Open Follow-Ups

The current stack proves the bundle path first. Two follow-ups should stay
visible during review:

* expose direct VM `save-state` and `restore-state` operations on the VM
  resource so VM state is usable without a full bundle;
* keep browser harness coverage for export, import, VM state, cooperative task
  checkpoints, and fail-closed unsupported task state as the proposal evolves.

These follow-ups refine the API surface. They do not change the core contract:
bundle migration is for checkpoint-aware workloads with explicit state payloads.
