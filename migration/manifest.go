package migration

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"time"
)

var (
	ErrUnsupported       = errors.New("migration unsupported")
	ErrInFlight          = errors.New("migration in flight")
	ErrUnknownComponent  = errors.New("migration unknown component")
	ErrUnknownFilesystem = errors.New("migration unknown filesystem")
	ErrInvalidManifest   = errors.New("migration invalid manifest")
	ErrUnrestorableFD    = errors.New("migration unrestorable file descriptor")
)

const BundleManifestVersion = "wanix-migration-v1"

type Phase string

const (
	PhasePrepare    Phase = "prepare"
	PhaseQuiesce    Phase = "quiesce"
	PhaseCheckpoint Phase = "checkpoint"
	PhaseResume     Phase = "resume"
	PhaseRestore    Phase = "restore"
)

type Mode string

const (
	ModeMigrate Mode = "migrate"
	ModeFork    Mode = "fork"
)

type Context struct {
	Phase    Phase             `json:"phase,omitempty"`
	Mode     Mode              `json:"mode,omitempty"`
	Deadline time.Time         `json:"deadline,omitempty"`
	Values   map[string]string `json:"values,omitempty"`
}

type Writer struct {
	io.Writer
}

type Reader struct {
	io.Reader
}

type Component interface {
	Descriptor() ComponentDescriptor
	Prepare(*Context) error
	Quiesce(*Context) error
	Checkpoint(*Writer) error
	Resume(*Context) error
}

type Restorer interface {
	Restore(*Reader, ComponentDescriptor) (Component, error)
}

type ComponentDescriptor struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Version string            `json:"version,omitempty"`
	Depends []string          `json:"depends,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type FilesystemDescriptor struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Source      string            `json:"source,omitempty"`
	ReadOnly    bool              `json:"read_only,omitempty"`
	BaseFSID    string            `json:"base_fs_id,omitempty"`
	OverlayFSID string            `json:"overlay_fs_id,omitempty"`
	WhiteoutDir string            `json:"whiteout_dir,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type BundleManifest struct {
	Version     string                 `json:"version"`
	Mode        Mode                   `json:"mode"`
	CreatedAt   time.Time              `json:"created_at"`
	Components  []ComponentDescriptor  `json:"components,omitempty"`
	Filesystems []FilesystemDescriptor `json:"filesystems,omitempty"`
	Tasks       []TaskManifest         `json:"tasks,omitempty"`
	VMs         []VMManifest           `json:"vms,omitempty"`
	Workers     []WorkerManifest       `json:"workers,omitempty"`
}

func ValidateBundleManifest(manifest BundleManifest) error {
	if manifest.Version != BundleManifestVersion {
		return fmt.Errorf("bundle manifest version %q: %w", manifest.Version, ErrInvalidManifest)
	}
	switch manifest.Mode {
	case ModeMigrate, ModeFork:
	default:
		return fmt.Errorf("bundle manifest mode %q: %w", manifest.Mode, ErrInvalidManifest)
	}
	if err := validateComponents(manifest.Components); err != nil {
		return err
	}
	if err := validateFilesystems(manifest.Filesystems); err != nil {
		return err
	}
	if err := validateTasks(manifest.Tasks); err != nil {
		return err
	}
	if err := validateVMs(manifest.VMs); err != nil {
		return err
	}
	if err := validateWorkers(manifest.Workers); err != nil {
		return err
	}
	return nil
}

func validateComponents(components []ComponentDescriptor) error {
	seen := make(map[string]bool, len(components))
	for _, component := range components {
		if component.ID == "" {
			return fmt.Errorf("bundle manifest component id: %w", ErrInvalidManifest)
		}
		if seen[component.ID] {
			return fmt.Errorf("bundle manifest component %q duplicate: %w", component.ID, ErrInvalidManifest)
		}
		seen[component.ID] = true
		for _, dep := range component.Depends {
			if dep == "" {
				return fmt.Errorf("bundle manifest component %q dependency: %w", component.ID, ErrInvalidManifest)
			}
		}
	}
	return nil
}

func validateFilesystems(filesystems []FilesystemDescriptor) error {
	seen := make(map[string]bool, len(filesystems))
	for _, fsys := range filesystems {
		if fsys.ID == "" {
			return fmt.Errorf("bundle manifest filesystem id: %w", ErrInvalidManifest)
		}
		if seen[fsys.ID] {
			return fmt.Errorf("bundle manifest filesystem %q duplicate: %w", fsys.ID, ErrInvalidManifest)
		}
		seen[fsys.ID] = true
		if fsys.Source != "" && !iofs.ValidPath(fsys.Source) {
			return fmt.Errorf("bundle manifest filesystem %q source %q: %w", fsys.ID, fsys.Source, ErrInvalidManifest)
		}
	}
	return nil
}

func validateTasks(tasks []TaskManifest) error {
	seen := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			return fmt.Errorf("bundle manifest task id: %w", ErrInvalidManifest)
		}
		if seen[task.ID] {
			return fmt.Errorf("bundle manifest task %q duplicate: %w", task.ID, ErrInvalidManifest)
		}
		seen[task.ID] = true
		if task.Kind == "" {
			return fmt.Errorf("bundle manifest task %q kind: %w", task.ID, ErrInvalidManifest)
		}
		switch task.State {
		case "", TaskStateCreated, TaskStateRunning, TaskStateExited:
		default:
			return fmt.Errorf("bundle manifest task %q state %q: %w", task.ID, task.State, ErrInvalidManifest)
		}
		if task.StatePath != "" && !iofs.ValidPath(task.StatePath) {
			return fmt.Errorf("bundle manifest task %q state path %q: %w", task.ID, task.StatePath, ErrInvalidManifest)
		}
		if err := validateNamespace(task.ID, task.Namespace); err != nil {
			return err
		}
		if err := validateFDs(task.ID, task.FDs); err != nil {
			return err
		}
	}
	return nil
}

func validateNamespace(taskID string, namespace NamespaceManifest) error {
	if namespace.TaskID != "" && namespace.TaskID != taskID {
		return fmt.Errorf("bundle manifest task %q namespace task %q: %w", taskID, namespace.TaskID, ErrInvalidManifest)
	}
	indexes := make(map[string]map[int]bool)
	for _, bind := range namespace.Binds {
		if !iofs.ValidPath(bind.DstPath) {
			return fmt.Errorf("bundle manifest task %q bind destination %q: %w", taskID, bind.DstPath, ErrInvalidManifest)
		}
		if !iofs.ValidPath(bind.SrcPath) {
			return fmt.Errorf("bundle manifest task %q bind source %q: %w", taskID, bind.SrcPath, ErrInvalidManifest)
		}
		if bind.SrcFSID == "" {
			return fmt.Errorf("bundle manifest task %q bind %q filesystem: %w", taskID, bind.DstPath, ErrInvalidManifest)
		}
		switch bind.Mode {
		case "after", "before", "replace":
		default:
			return fmt.Errorf("bundle manifest task %q bind %q mode %q: %w", taskID, bind.DstPath, bind.Mode, ErrInvalidManifest)
		}
		if bind.Index < 0 {
			return fmt.Errorf("bundle manifest task %q bind %q index %d: %w", taskID, bind.DstPath, bind.Index, ErrInvalidManifest)
		}
		if indexes[bind.DstPath] == nil {
			indexes[bind.DstPath] = make(map[int]bool)
		}
		if indexes[bind.DstPath][bind.Index] {
			return fmt.Errorf("bundle manifest task %q bind %q index %d duplicate: %w", taskID, bind.DstPath, bind.Index, ErrInvalidManifest)
		}
		indexes[bind.DstPath][bind.Index] = true
	}
	for dstPath, seen := range indexes {
		for i := 0; i < len(seen); i++ {
			if !seen[i] {
				return fmt.Errorf("bundle manifest task %q bind %q index %d missing: %w", taskID, dstPath, i, ErrInvalidManifest)
			}
		}
	}
	return nil
}

func validateFDs(taskID string, fds []FDManifest) error {
	seen := make(map[int]bool, len(fds))
	for _, fd := range fds {
		if fd.FD < 0 {
			return fmt.Errorf("bundle manifest task %q fd %d: %w", taskID, fd.FD, ErrInvalidManifest)
		}
		if seen[fd.FD] {
			return fmt.Errorf("bundle manifest task %q fd %d duplicate: %w", taskID, fd.FD, ErrInvalidManifest)
		}
		seen[fd.FD] = true
		if fd.Restorable && fd.Path == "" {
			return fmt.Errorf("bundle manifest task %q fd %d path: %w", taskID, fd.FD, ErrInvalidManifest)
		}
		if fd.Path != "" && !iofs.ValidPath(fd.Path) {
			return fmt.Errorf("bundle manifest task %q fd %d path %q: %w", taskID, fd.FD, fd.Path, ErrInvalidManifest)
		}
	}
	return nil
}

func validateVMs(vms []VMManifest) error {
	seen := make(map[string]bool, len(vms))
	for _, vm := range vms {
		if vm.ID == "" {
			return fmt.Errorf("bundle manifest vm id: %w", ErrInvalidManifest)
		}
		if seen[vm.ID] {
			return fmt.Errorf("bundle manifest vm %q duplicate: %w", vm.ID, ErrInvalidManifest)
		}
		seen[vm.ID] = true
		if vm.Kind == "" {
			return fmt.Errorf("bundle manifest vm %q kind: %w", vm.ID, ErrInvalidManifest)
		}
		if vm.StatePath != "" && !iofs.ValidPath(vm.StatePath) {
			return fmt.Errorf("bundle manifest vm %q state path %q: %w", vm.ID, vm.StatePath, ErrInvalidManifest)
		}
	}
	return nil
}

func validateWorkers(workers []WorkerManifest) error {
	seen := make(map[string]bool, len(workers))
	for _, worker := range workers {
		if worker.ID == "" {
			return fmt.Errorf("bundle manifest worker id: %w", ErrInvalidManifest)
		}
		if seen[worker.ID] {
			return fmt.Errorf("bundle manifest worker %q duplicate: %w", worker.ID, ErrInvalidManifest)
		}
		seen[worker.ID] = true
		if worker.Kind == "" {
			return fmt.Errorf("bundle manifest worker %q kind: %w", worker.ID, ErrInvalidManifest)
		}
		if worker.StatePath != "" && !iofs.ValidPath(worker.StatePath) {
			return fmt.Errorf("bundle manifest worker %q state path %q: %w", worker.ID, worker.StatePath, ErrInvalidManifest)
		}
	}
	return nil
}

type TaskManifest struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind,omitempty"`
	State      TaskState         `json:"state,omitempty"`
	StatePath  string            `json:"state_path,omitempty"`
	Alias      string            `json:"alias,omitempty"`
	Command    string            `json:"command,omitempty"`
	Exit       string            `json:"exit,omitempty"`
	Directory  string            `json:"directory,omitempty"`
	Env        []string          `json:"env,omitempty"`
	ExportFSID string            `json:"export_fs_id,omitempty"`
	FDs        []FDManifest      `json:"fds,omitempty"`
	Namespace  NamespaceManifest `json:"namespace,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type TaskState string

const (
	TaskStateCreated TaskState = "created"
	TaskStateRunning TaskState = "running"
	TaskStateExited  TaskState = "exited"
)

type FDManifest struct {
	FD         int    `json:"fd"`
	Kind       string `json:"kind,omitempty"`
	Path       string `json:"path,omitempty"`
	Flags      int    `json:"flags,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Stdio      bool   `json:"stdio,omitempty"`
	Restorable bool   `json:"restorable"`
	Error      string `json:"error,omitempty"`
}

type NamespaceManifest struct {
	TaskID string         `json:"task_id,omitempty"`
	Binds  []BindManifest `json:"binds,omitempty"`
}

type BindManifest struct {
	DstPath string `json:"dst_path"`
	SrcFSID string `json:"src_fs_id"`
	SrcPath string `json:"src_path"`
	Mode    string `json:"mode"`
	Index   int    `json:"index"`
	Root    bool   `json:"root,omitempty"`
	System  bool   `json:"system,omitempty"`
}

type WorkerManifest struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind,omitempty"`
	StatePath string            `json:"state_path,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type VMManifest struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind,omitempty"`
	StatePath string            `json:"state_path,omitempty"`
	GuestFSID string            `json:"guest_fs_id,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}
