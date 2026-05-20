package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"tractor.dev/toolkit-go/duplex/rpc"
	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/cowfs"
	"tractor.dev/wanix/fs/vfs"
	"tractor.dev/wanix/migration"
	"tractor.dev/wanix/vm"
)

func (s *syscaller) bundleManifest(r rpc.Responder, c *rpc.Call) {
	var args []any
	c.Receive(&args)

	descs, err := decodeJSONArg[[]migration.FilesystemDescriptor](args, 0, nil)
	if err != nil {
		r.Return(err)
		return
	}
	resolver, err := bundleFilesystemResolver(s.task.Context(), s.task.NS(), descs)
	if err != nil {
		r.Return(err)
		return
	}
	manifest, err := s.task.Root().BundleManifest(wanix.BundleManifestOptions{
		Filesystems: descs,
		Resolve:     resolver,
	})
	if err != nil {
		r.Return(err)
		return
	}
	r.Return(manifest)
}

func (s *syscaller) restoreBundleManifest(r rpc.Responder, c *rpc.Call) {
	var args []any
	c.Receive(&args)

	manifest, err := decodeJSONArg[migration.BundleManifest](args, 0, migration.BundleManifest{})
	if err != nil {
		r.Return(err)
		return
	}
	if err := wanix.ValidateBundleRestore(manifest); err != nil {
		r.Return(err)
		return
	}
	lookup, err := bundleFilesystemLookup(s.task.Context(), s.task.NS(), manifest.Filesystems)
	if err != nil {
		r.Return(err)
		return
	}
	restored, err := s.task.Root().RestoreBundleManifest(s.task.Context(), manifest, lookup, wanix.BundleRestoreOptions{
		RestoreVM: func(ctx context.Context, manifest migration.VMManifest, lookup vfs.FSIDLookup) (string, func(), error) {
			return importBundleVM(ctx, s.task.Root(), manifest, lookup)
		},
	})
	if err != nil {
		r.Return(err)
		return
	}
	ids := make([]string, 0, len(restored.Tasks))
	for _, task := range restored.Tasks {
		ids = append(ids, task.ID())
	}
	r.Return(map[string]any{"tasks": ids, "vms": restored.VMs})
}

func importBundleVM(ctx context.Context, root *wanix.Task, manifest migration.VMManifest, lookup vfs.FSIDLookup) (string, func(), error) {
	device, name, err := fs.ResolveTo[*vm.Device](root.NS(), ctx, "#vm")
	if err != nil {
		return "", nil, fmt.Errorf("restore bundle vms: %w", err)
	}
	if name != "." {
		return "", nil, fmt.Errorf("restore bundle vms resolved to %s: %w", name, fs.ErrInvalid)
	}
	var restored *vm.VM
	if manifest.StatePath != "" {
		state, err := fs.ReadFile(root.NS(), manifest.StatePath)
		if err != nil {
			return "", nil, fmt.Errorf("restore bundle vm %s state %s: %w", manifest.ID, manifest.StatePath, err)
		}
		restored, err = device.ImportManifestWithState(manifest, state)
	} else {
		restored, err = device.ImportManifest(manifest)
	}
	if err != nil {
		return "", nil, err
	}
	if manifest.GuestFSID != "" {
		if lookup == nil {
			device.Remove(restored.ID())
			return "", nil, fmt.Errorf("restore bundle vm %s guest filesystem %s: %w", manifest.ID, manifest.GuestFSID, migration.ErrUnknownFilesystem)
		}
		guest, err := lookup(manifest.GuestFSID)
		if err != nil {
			device.Remove(restored.ID())
			return "", nil, fmt.Errorf("restore bundle vm %s guest filesystem %s: %w", manifest.ID, manifest.GuestFSID, err)
		}
		if guest == nil {
			device.Remove(restored.ID())
			return "", nil, fmt.Errorf("restore bundle vm %s guest filesystem %s: %w", manifest.ID, manifest.GuestFSID, migration.ErrUnknownFilesystem)
		}
		if err := restored.SetGuest(guest); err != nil {
			device.Remove(restored.ID())
			return "", nil, fmt.Errorf("restore bundle vm %s guest filesystem %s: %w", manifest.ID, manifest.GuestFSID, err)
		}
	}
	return restored.ID(), func() { device.Remove(restored.ID()) }, nil
}

type bundleFilesystemRef struct {
	id   string
	fsys fs.FS
}

func bundleFilesystemResolver(ctx context.Context, ns *vfs.NS, descs []migration.FilesystemDescriptor) (vfs.FSIDResolver, error) {
	refs, err := bundleFilesystemRefs(ctx, ns, descs)
	if err != nil {
		return nil, err
	}
	return func(candidate fs.FS) (string, error) {
		for _, ref := range refs {
			if sameBundleFilesystem(ref.fsys, candidate) {
				return ref.id, nil
			}
		}
		return "", migration.ErrUnknownFilesystem
	}, nil
}

func sameBundleFilesystem(a, b fs.FS) bool {
	if a == nil || b == nil {
		return a == b
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta.Comparable() {
		return a == b
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	switch va.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return va.Pointer() == vb.Pointer()
	default:
		return false
	}
}

func bundleFilesystemLookup(ctx context.Context, ns *vfs.NS, descs []migration.FilesystemDescriptor) (vfs.FSIDLookup, error) {
	refs, err := bundleExternalFilesystemRefs(ctx, ns, descs)
	if err != nil {
		return nil, err
	}
	return func(id string) (fs.FS, error) {
		for _, ref := range refs {
			if ref.id == id {
				return ref.fsys, nil
			}
		}
		return nil, migration.ErrUnknownFilesystem
	}, nil
}

func bundleExternalFilesystemRefs(ctx context.Context, ns *vfs.NS, descs []migration.FilesystemDescriptor) ([]bundleFilesystemRef, error) {
	external := descs[:0:0]
	for _, desc := range descs {
		if desc.Kind == cowfs.FilesystemKind {
			continue
		}
		external = append(external, desc)
	}
	return bundleFilesystemRefs(ctx, ns, external)
}

func bundleFilesystemRefs(ctx context.Context, ns *vfs.NS, descs []migration.FilesystemDescriptor) ([]bundleFilesystemRef, error) {
	seen := map[string]bool{}
	refs := make([]bundleFilesystemRef, 0, len(descs))
	for _, desc := range descs {
		if desc.ID == "" {
			return nil, fmt.Errorf("bundle filesystem source: %w", migration.ErrUnknownFilesystem)
		}
		if seen[desc.ID] {
			return nil, fmt.Errorf("bundle filesystem source %s: %w", desc.ID, fs.ErrExist)
		}
		seen[desc.ID] = true
		if desc.Source == "" {
			return nil, fmt.Errorf("bundle filesystem %s source: %w", desc.ID, fs.ErrInvalid)
		}
		fsys, name, err := fs.Resolve(ns, ctx, desc.Source)
		if err != nil {
			return nil, fmt.Errorf("bundle filesystem %s source %s: %w", desc.ID, desc.Source, err)
		}
		if name != "." {
			info, err := fs.StatContext(ctx, fsys, name)
			if err != nil {
				return nil, fmt.Errorf("bundle filesystem %s source %s: %v: %w", desc.ID, desc.Source, err, fs.ErrInvalid)
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("bundle filesystem %s source %s resolved to %s: %w", desc.ID, desc.Source, name, fs.ErrInvalid)
			}
			fsys, err = fs.Sub(fsys, name)
			if err != nil {
				return nil, fmt.Errorf("bundle filesystem %s source %s: %w", desc.ID, desc.Source, err)
			}
		}
		refs = append(refs, bundleFilesystemRef{id: desc.ID, fsys: fsys})
	}
	return refs, nil
}

func decodeJSONArg[T any](args []any, idx int, def T) (T, error) {
	if len(args) <= idx {
		return def, nil
	}
	var data []byte
	switch v := args[idx].(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return def, errInvalidArg("bundle", "json")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return def, nil
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return def, err
	}
	return out, nil
}
