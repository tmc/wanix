package migration

import (
	"errors"
	"fmt"
	"io"
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
		return nil
	default:
		return fmt.Errorf("bundle manifest mode %q: %w", manifest.Mode, ErrInvalidManifest)
	}
}

type TaskManifest struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind,omitempty"`
	Alias     string            `json:"alias,omitempty"`
	Command   string            `json:"command,omitempty"`
	Directory string            `json:"directory,omitempty"`
	Env       []string          `json:"env,omitempty"`
	FDs       []FDManifest      `json:"fds,omitempty"`
	Namespace NamespaceManifest `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type FDManifest struct {
	FD         int    `json:"fd"`
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
	Labels    map[string]string `json:"labels,omitempty"`
}
