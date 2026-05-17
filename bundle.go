package wanix

import (
	"context"
	"fmt"
	"sort"
	"time"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/cowfs"
	"tractor.dev/wanix/fs/vfs"
	"tractor.dev/wanix/migration"
)

const BundleManifestVersion = "wanix-migration-v1"

// BundleManifestOptions configures task filesystem manifest export.
type BundleManifestOptions struct {
	Version     string
	Mode        migration.Mode
	CreatedAt   time.Time
	Components  []migration.ComponentDescriptor
	Filesystems []migration.FilesystemDescriptor
	Resolve     vfs.FSIDResolver
}

// VMRestoreFunc restores a VM resource descriptor and returns an undo function.
type VMRestoreFunc func(context.Context, migration.VMManifest) (id string, rollback func(), err error)

// BundleRestoreOptions provides already-materialized filesystems for bundle restore.
type BundleRestoreOptions struct {
	TaskFS      *TaskFS
	Filesystems map[string]fs.FS
	RestoreVM   VMRestoreFunc
}

// BundleRestore is the result of restoring a migration bundle manifest.
type BundleRestore struct {
	TaskFS      *TaskFS
	Filesystems map[string]fs.FS
	Tasks       []*Task
	VMs         []string
}

// BundleManifest returns a migration manifest for the task filesystem.
//
// The caller supplies the filesystem descriptors and resolver because only the
// embedding runtime knows how a filesystem should be serialized or reconnected.
// Live worker handles and task exports are rejected until those resource layers
// have migration descriptors.
func (d *TaskFS) BundleManifest(opts BundleManifestOptions) (migration.BundleManifest, error) {
	if opts.Resolve == nil {
		return migration.BundleManifest{}, fmt.Errorf("export bundle filesystem resolver: %w", migration.ErrUnknownFilesystem)
	}
	version := opts.Version
	if version == "" {
		version = BundleManifestVersion
	}
	mode := opts.Mode
	if mode == "" {
		mode = migration.ModeMigrate
	}
	created := opts.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}

	tasks := d.tasksSnapshot()
	out := migration.BundleManifest{
		Version:     version,
		Mode:        mode,
		CreatedAt:   created,
		Components:  append([]migration.ComponentDescriptor(nil), opts.Components...),
		Filesystems: append([]migration.FilesystemDescriptor(nil), opts.Filesystems...),
		Tasks:       make([]migration.TaskManifest, 0, len(tasks)),
	}
	for _, task := range tasks {
		if err := task.checkBundleExportable(); err != nil {
			return migration.BundleManifest{}, err
		}
		manifest, err := task.Manifest(opts.Resolve)
		if err != nil {
			return migration.BundleManifest{}, err
		}
		out.Tasks = append(out.Tasks, manifest)
	}
	return out, nil
}

func (d *TaskFS) tasksSnapshot() []*Task {
	d.mu.Lock()
	ids := make([]string, 0, len(d.resources))
	for id := range d.resources {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return numericIDLess(ids[i], ids[j])
	})
	tasks := make([]*Task, 0, len(ids))
	for _, id := range ids {
		if task, ok := d.resources[id].(*Task); ok {
			tasks = append(tasks, task)
		}
	}
	d.mu.Unlock()
	return tasks
}

func numericIDLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func (r *Task) checkBundleExportable() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.worker != nil {
		return fmt.Errorf("export task %s worker: %w", r.ID(), migration.ErrUnsupported)
	}
	if r.export != nil {
		return fmt.Errorf("export task %s filesystem export: %w", r.ID(), migration.ErrUnsupported)
	}
	return nil
}

// RestoreBundle restores filesystems and tasks described by manifest.
//
// Non-cowfs filesystem descriptors must already be materialized in opts by ID.
// Cowfs descriptors are rebuilt from their base and overlay filesystem IDs.
// VM descriptors are restored through opts.RestoreVM when present. Worker
// manifests still fail closed.
func RestoreBundle(ctx context.Context, manifest migration.BundleManifest, opts BundleRestoreOptions) (*BundleRestore, error) {
	taskfs := opts.TaskFS
	if taskfs == nil {
		taskfs = NewTaskFS()
	}
	external := func(id string) (fs.FS, error) {
		fsys, ok := opts.Filesystems[id]
		if !ok || fsys == nil {
			return nil, migration.ErrUnknownFilesystem
		}
		return fsys, nil
	}
	return taskfs.restoreBundle(ctx, manifest, nil, external, opts)
}

func (d *TaskFS) restoreBundle(ctx context.Context, manifest migration.BundleManifest, parent *Task, external vfs.FSIDLookup, opts BundleRestoreOptions) (*BundleRestore, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkBundleRestoreUnsupported(manifest); err != nil {
		return nil, err
	}
	vms, err := restoreBundleVMs(ctx, manifest.VMs, opts.RestoreVM)
	if err != nil {
		return nil, err
	}
	filesystems, lookup, err := restoreBundleFilesystems(manifest.Filesystems, external)
	if err != nil {
		rollbackBundleVMs(vms)
		return nil, err
	}
	taskManifest := manifest
	taskManifest.VMs = nil
	tasks, err := d.importBundleManifest(ctx, taskManifest, parent, lookup)
	if err != nil {
		rollbackBundleVMs(vms)
		return nil, err
	}
	vmIDs := make([]string, 0, len(vms))
	for _, vm := range vms {
		vmIDs = append(vmIDs, vm.id)
	}
	return &BundleRestore{
		TaskFS:      d,
		Filesystems: filesystems,
		Tasks:       tasks,
		VMs:         vmIDs,
	}, nil
}

// ImportBundleManifest restores all task manifests in a bundle.
func (d *TaskFS) ImportBundleManifest(ctx context.Context, manifest migration.BundleManifest, parent *Task, lookup vfs.FSIDLookup) ([]*Task, error) {
	if err := checkBundleTaskUnsupported(manifest); err != nil {
		return nil, err
	}
	_, bundleLookup, err := restoreBundleFilesystems(manifest.Filesystems, lookup)
	if err != nil {
		return nil, err
	}
	return d.importBundleManifest(ctx, manifest, parent, bundleLookup)
}

func (d *TaskFS) importBundleManifest(ctx context.Context, manifest migration.BundleManifest, parent *Task, lookup vfs.FSIDLookup) ([]*Task, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkBundleTaskUnsupported(manifest); err != nil {
		return nil, err
	}
	startNextID := d.currentNextID()
	tasks := make([]*Task, 0, len(manifest.Tasks))
	for _, taskManifest := range manifest.Tasks {
		task, err := d.ImportManifest(ctx, taskManifest, parent, lookup)
		if err != nil {
			d.rollbackImportedTasks(tasks, startNextID)
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (d *TaskFS) currentNextID() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nextID
}

func (d *TaskFS) rollbackImportedTasks(tasks []*Task, nextID int) {
	for i := len(tasks) - 1; i >= 0; i-- {
		task := tasks[i]
		task.mu.Lock()
		fds := task.fds
		task.fds = nil
		alias := task.alias
		task.mu.Unlock()

		closeOpenFiles(fds)

		d.mu.Lock()
		id := task.ID()
		if current, ok := d.resources[id]; ok && current == task {
			delete(d.resources, id)
		}
		if alias != "" {
			if current, ok := d.aliases[alias]; ok && current == task {
				delete(d.aliases, alias)
			}
		}
		d.mu.Unlock()
	}
	d.mu.Lock()
	d.nextID = nextID
	d.mu.Unlock()
}

func checkBundleTaskUnsupported(manifest migration.BundleManifest) error {
	return checkBundleUnsupported(manifest, false)
}

func checkBundleRestoreUnsupported(manifest migration.BundleManifest) error {
	return checkBundleUnsupported(manifest, true)
}

func checkBundleUnsupported(manifest migration.BundleManifest, allowVMs bool) error {
	if len(manifest.VMs) != 0 {
		if !allowVMs {
			return fmt.Errorf("restore bundle vms: %w", migration.ErrUnsupported)
		}
		for _, vm := range manifest.VMs {
			if vm.StatePath != "" {
				return fmt.Errorf("restore bundle vm %s state: %w", vm.ID, migration.ErrUnsupported)
			}
		}
	}
	if len(manifest.Workers) != 0 {
		return fmt.Errorf("restore bundle workers: %w", migration.ErrUnsupported)
	}
	for _, component := range manifest.Components {
		switch component.Kind {
		case "", "task", "filesystem", cowfs.FilesystemKind:
		default:
			return fmt.Errorf("restore bundle component %s kind %q: %w", component.ID, component.Kind, migration.ErrUnsupported)
		}
	}
	return nil
}

type restoredBundleVM struct {
	id       string
	rollback func()
}

func restoreBundleVMs(ctx context.Context, manifests []migration.VMManifest, restore VMRestoreFunc) ([]restoredBundleVM, error) {
	if len(manifests) == 0 {
		return nil, nil
	}
	if restore == nil {
		return nil, fmt.Errorf("restore bundle vms: %w", migration.ErrUnsupported)
	}
	restored := make([]restoredBundleVM, 0, len(manifests))
	for _, manifest := range manifests {
		id, rollback, err := restore(ctx, manifest)
		if err != nil {
			rollbackBundleVMs(restored)
			return nil, err
		}
		if id == "" {
			id = manifest.ID
		}
		restored = append(restored, restoredBundleVM{id: id, rollback: rollback})
	}
	return restored, nil
}

func rollbackBundleVMs(vms []restoredBundleVM) {
	for i := len(vms) - 1; i >= 0; i-- {
		if vms[i].rollback != nil {
			vms[i].rollback()
		}
	}
}

func restoreBundleFilesystems(descs []migration.FilesystemDescriptor, external vfs.FSIDLookup) (map[string]fs.FS, vfs.FSIDLookup, error) {
	descriptors := make(map[string]migration.FilesystemDescriptor, len(descs))
	restored := make(map[string]fs.FS, len(descs))
	for _, desc := range descs {
		if desc.ID == "" {
			return nil, nil, fmt.Errorf("restore bundle filesystem: %w", migration.ErrUnknownFilesystem)
		}
		if _, exists := descriptors[desc.ID]; exists {
			return nil, nil, fmt.Errorf("restore bundle filesystem %s: %w", desc.ID, fs.ErrExist)
		}
		descriptors[desc.ID] = desc
	}

	visiting := make(map[string]bool)
	var lookup vfs.FSIDLookup
	lookup = func(id string) (fs.FS, error) {
		if id == "" {
			return nil, migration.ErrUnknownFilesystem
		}
		if fsys, ok := restored[id]; ok {
			return fsys, nil
		}
		if desc, ok := descriptors[id]; ok {
			if visiting[id] {
				return nil, fmt.Errorf("restore bundle filesystem %s: %w", id, fs.ErrInvalid)
			}
			visiting[id] = true
			defer delete(visiting, id)
			fsys, err := restoreBundleFilesystem(desc, lookup, external)
			if err != nil {
				return nil, err
			}
			restored[id] = fsys
			return fsys, nil
		}
		if external == nil {
			return nil, migration.ErrUnknownFilesystem
		}
		fsys, err := external(id)
		if err != nil {
			return nil, err
		}
		if fsys == nil {
			return nil, migration.ErrUnknownFilesystem
		}
		restored[id] = fsys
		return fsys, nil
	}

	for _, desc := range descs {
		if _, err := lookup(desc.ID); err != nil {
			return nil, nil, err
		}
	}
	return restored, lookup, nil
}

func restoreBundleFilesystem(desc migration.FilesystemDescriptor, lookup vfs.FSIDLookup, external vfs.FSIDLookup) (fs.FS, error) {
	if desc.Kind == cowfs.FilesystemKind {
		return cowfs.RestoreDescriptor(desc, lookup)
	}
	if external != nil {
		fsys, err := external(desc.ID)
		if err == nil && fsys != nil {
			return fsys, nil
		}
	}
	return nil, fmt.Errorf("restore bundle filesystem %s kind %q: %w", desc.ID, desc.Kind, migration.ErrUnsupported)
}
