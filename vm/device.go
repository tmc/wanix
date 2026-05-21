package vm

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/migration"
)

const aliasLabel = "alias"

type Device struct {
	resources map[string]fs.FS
	aliases   map[string]fs.FS
	nextID    int
	root      *wanix.Task
	mu        sync.Mutex
}

func Drivers(t *wanix.Task) []string {
	b, err := t.NS().Binds("#vm")
	if err != nil {
		log.Println("vm drivers:", err)
		return nil
	}
	result := make([]string, 0, len(b))
	for _, b := range b {
		name := strings.TrimSuffix(b.Name(), filepath.Ext(b.Name()))
		result = append(result, name)
	}
	return result
}

func New(root *wanix.Task) *Device {
	d := &Device{
		resources: make(map[string]fs.FS),
		aliases:   make(map[string]fs.FS),
		nextID:    0,
		root:      root,
	}
	return d
}

func (d *Device) Alloc(kind string) (wanix.Resource, error) {
	if !slices.Contains(Drivers(d.root), kind) {
		return nil, fs.ErrNotExist
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	rid := strconv.Itoa(d.nextID)
	r := &VM{
		id:     rid,
		kind:   kind,
		device: d,
	}
	d.resources[rid] = r
	return r, nil
}

// ImportManifest restores a VM resource descriptor without starting it.
func (d *Device) ImportManifest(manifest migration.VMManifest) (*VM, error) {
	if manifest.StatePath != "" {
		return nil, fmt.Errorf("import vm %s state: %w", manifest.ID, migration.ErrUnsupported)
	}
	return d.importManifest(manifest, nil)
}

// ImportManifestWithState restores a VM descriptor with its serialized state.
func (d *Device) ImportManifestWithState(manifest migration.VMManifest, state []byte) (*VM, error) {
	if manifest.StatePath == "" {
		return nil, fmt.Errorf("import vm %s state: %w", manifest.ID, fs.ErrInvalid)
	}
	if state == nil {
		return nil, fmt.Errorf("import vm %s state %s: %w", manifest.ID, manifest.StatePath, fs.ErrInvalid)
	}
	return d.importManifest(manifest, state)
}

func (d *Device) importManifest(manifest migration.VMManifest, state []byte) (*VM, error) {
	id, err := strconv.Atoi(manifest.ID)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("import vm %q: %w", manifest.ID, fs.ErrInvalid)
	}
	if manifest.Kind == "" {
		return nil, fmt.Errorf("import vm %s: %w", manifest.ID, fs.ErrInvalid)
	}
	if !slices.Contains(Drivers(d.root), manifest.Kind) {
		return nil, fmt.Errorf("import vm %s kind %q: %w", manifest.ID, manifest.Kind, fs.ErrNotExist)
	}
	alias := manifest.Labels[aliasLabel]

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.resources[manifest.ID]; exists {
		return nil, fmt.Errorf("import vm %s: %w", manifest.ID, fs.ErrExist)
	}
	if alias != "" {
		if _, exists := d.aliases[alias]; exists {
			return nil, fmt.Errorf("import vm alias %q: %w", alias, fs.ErrExist)
		}
	}
	r := &VM{
		id:     manifest.ID,
		alias:  alias,
		kind:   manifest.Kind,
		state:  append([]byte(nil), state...),
		device: d,
	}
	d.resources[manifest.ID] = r
	if alias != "" {
		d.aliases[alias] = r
	}
	if d.nextID < id {
		d.nextID = id
	}
	return r, nil
}

// Remove deletes a VM resource by ID and rewinds nextID to the remaining maximum.
func (d *Device) Remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.resources[id]
	if !ok {
		return
	}
	delete(d.resources, id)
	if vm, ok := r.(*VM); ok && vm.alias != "" {
		if current, ok := d.aliases[vm.alias]; ok && current == r {
			delete(d.aliases, vm.alias)
		}
	}
	d.nextID = 0
	for rid := range d.resources {
		n, err := strconv.Atoi(rid)
		if err == nil && d.nextID < n {
			d.nextID = n
		}
	}
}

// i really wish we could just get back a type from path via resolve
// but for now we have to look it up manually
func (d *Device) Lookup(rid string) (*VM, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tt, ok := d.resources[rid]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return tt.(*VM), nil
}

func (d *Device) Open(name string) (fs.File, error) {
	return d.OpenContext(context.Background(), name)
}

func (d *Device) ResolveFS(ctx context.Context, name string) (fs.FS, string, error) {
	base, rest, ok := strings.Cut(name, "/")
	if !ok {
		if _, exists := d.rootFS().(fskit.MapFS)[name]; exists {
			return d, name, nil
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if r, exists := d.resources[name]; exists {
			return r, ".", nil
		}
		if r, exists := d.aliases[name]; exists {
			return r, ".", nil
		}
		return d, name, nil
	}
	if base == "new" {
		return d, name, nil
	}
	d.mu.Lock()
	r, exists := d.resources[base]
	if !exists {
		r, exists = d.aliases[base]
	}
	d.mu.Unlock()
	if !exists {
		return d, name, nil
	}
	return fs.Resolve(r, ctx, rest)
}

func (d *Device) rootFS() fs.FS {
	return fskit.MapFS{
		"new": fskit.OpenFunc(func(ctx context.Context, name string) (fs.File, error) {
			if name == "." {
				var nodes []fs.DirEntry
				for _, kind := range Drivers(d.root) {
					nodes = append(nodes, fskit.Entry(kind, 0555))
				}
				return fskit.DirFile(fskit.Entry("new", 0555), nodes...), nil
			}
			return &fskit.FuncFile{
				Node: fskit.Entry(name, 0555),
				ReadFunc: func(n *fskit.Node) error {
					r, err := d.Alloc(name)
					if err != nil {
						return err
					}
					fskit.SetData(n, []byte(r.ID()+"\n"))
					return nil
				},
			}, nil
		}),
	}
}

func (d *Device) OpenContext(ctx context.Context, name string) (fs.File, error) {
	return fs.OpenContext(ctx, fskit.UnionFS{d.rootFS(), fskit.MapFS(d.resources), fskit.MapFS(d.aliases)}, name)
}
