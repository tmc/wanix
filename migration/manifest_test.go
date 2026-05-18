package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBundleManifestRoundTrip(t *testing.T) {
	created := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	in := BundleManifest{
		Version:   BundleManifestVersion,
		Mode:      ModeMigrate,
		CreatedAt: created,
		Components: []ComponentDescriptor{{
			ID:      "task/1",
			Kind:    "task",
			Version: "v1",
		}},
		Filesystems: []FilesystemDescriptor{{
			ID:          "rootfs",
			Kind:        "cowfs",
			BaseFSID:    "basefs",
			OverlayFSID: "overlayfs",
			WhiteoutDir: ".wh",
		}},
		Tasks: []TaskManifest{{
			ID:         "1",
			Kind:       "gojs",
			State:      TaskStateExited,
			Alias:      "shell",
			Command:    "rc",
			Exit:       "0",
			ExportFSID: "exportfs",
			FDs: []FDManifest{{
				FD:         3,
				Kind:       "file",
				Path:       "tmp/log",
				Flags:      2,
				Offset:     7,
				Restorable: true,
			}},
			Namespace: NamespaceManifest{
				TaskID: "1",
				Binds: []BindManifest{{
					DstPath: ".",
					SrcFSID: "rootfs",
					SrcPath: ".",
					Mode:    "replace",
					Root:    true,
				}},
			},
		}},
		VMs: []VMManifest{{
			ID:        "v86/1",
			Kind:      "v86",
			StatePath: "vm/1.state",
			GuestFSID: "guestfs",
		}},
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out BundleManifest
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Version != in.Version || out.Mode != in.Mode || !out.CreatedAt.Equal(created) {
		t.Fatalf("round trip changed header: %#v", out)
	}
	if len(out.Tasks) != 1 || len(out.Tasks[0].FDs) != 1 {
		t.Fatalf("round trip lost task fd manifests: %#v", out.Tasks)
	}
	if got := out.Tasks[0].FDs[0].Kind; got != "file" {
		t.Fatalf("fd kind = %q, want file", got)
	}
	if got := out.Tasks[0].Alias; got != "shell" {
		t.Fatalf("task alias = %q, want shell", got)
	}
	if got := out.Tasks[0].Exit; got != "0" {
		t.Fatalf("task exit = %q, want 0", got)
	}
	if got := out.Tasks[0].State; got != TaskStateExited {
		t.Fatalf("task state = %q, want exited", got)
	}
	if got := out.Tasks[0].ExportFSID; got != "exportfs" {
		t.Fatalf("task export fs id = %q, want exportfs", got)
	}
	if got := out.Filesystems[0].OverlayFSID; got != "overlayfs" {
		t.Fatalf("filesystem overlay id = %q, want overlayfs", got)
	}
	if got := out.Tasks[0].Namespace.Binds[0].SrcFSID; got != "rootfs" {
		t.Fatalf("namespace bind src fs id = %q, want rootfs", got)
	}
	if got := out.VMs[0].GuestFSID; got != "guestfs" {
		t.Fatalf("vm guest fs id = %q, want guestfs", got)
	}
}

func TestFailClosedErrorsWrap(t *testing.T) {
	err := fmt.Errorf("fd 4: %w", ErrUnrestorableFD)
	if !errors.Is(err, ErrUnrestorableFD) {
		t.Fatalf("errors.Is(%v, ErrUnrestorableFD) = false", err)
	}
	err = fmt.Errorf("v86 save: %w", ErrInFlight)
	if !errors.Is(err, ErrInFlight) {
		t.Fatalf("errors.Is(%v, ErrInFlight) = false", err)
	}
}

func TestBundleManifestFileRoundTrip(t *testing.T) {
	created := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	in := BundleManifest{
		Version:   BundleManifestVersion,
		Mode:      ModeFork,
		CreatedAt: created,
		Tasks: []TaskManifest{{
			ID:   "1",
			Kind: "auto",
		}},
	}
	if err := WriteBundleManifest(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadBundleManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != in.Version || out.Mode != in.Mode || !out.CreatedAt.Equal(created) || len(out.Tasks) != 1 {
		t.Fatalf("ReadBundleManifest = %#v, want %#v", out, in)
	}
	if err := WriteBundleManifest(dir, in); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second WriteBundleManifest error = %v, want ErrExist", err)
	}
}

func TestReadBundleManifestRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, BundleManifestFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBundleManifest(dir); err == nil {
		t.Fatal("ReadBundleManifest invalid json error = nil")
	}
}

func TestValidateBundleManifestRejectsInvalidHeader(t *testing.T) {
	for _, tt := range []struct {
		name     string
		manifest BundleManifest
	}{
		{
			name: "missing version",
			manifest: BundleManifest{
				Mode: ModeMigrate,
			},
		},
		{
			name: "wrong version",
			manifest: BundleManifest{
				Version: "wanix-migration-v0",
				Mode:    ModeMigrate,
			},
		},
		{
			name: "missing mode",
			manifest: BundleManifest{
				Version: BundleManifestVersion,
			},
		},
		{
			name: "unknown mode",
			manifest: BundleManifest{
				Version: BundleManifestVersion,
				Mode:    Mode("copy"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateBundleManifest(tt.manifest); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ValidateBundleManifest error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestValidateBundleManifestRejectsInvalidBody(t *testing.T) {
	valid := func() BundleManifest {
		return BundleManifest{
			Version: BundleManifestVersion,
			Mode:    ModeMigrate,
			Filesystems: []FilesystemDescriptor{{
				ID:     "rootfs",
				Kind:   "memfs",
				Source: "mnt",
			}},
			Tasks: []TaskManifest{{
				ID:    "1",
				Kind:  "auto",
				State: TaskStateCreated,
				Namespace: NamespaceManifest{
					TaskID: "1",
					Binds: []BindManifest{{
						DstPath: ".",
						SrcFSID: "rootfs",
						SrcPath: ".",
						Mode:    "replace",
						Index:   0,
						Root:    true,
					}},
				},
				FDs: []FDManifest{{
					FD:         3,
					Kind:       "file",
					Path:       "tmp/log",
					Restorable: true,
				}},
			}},
			VMs: []VMManifest{{
				ID:        "1",
				Kind:      "v86",
				StatePath: "vm/1.state",
			}},
			Workers: []WorkerManifest{{
				ID:        "browser",
				Kind:      "service-worker",
				StatePath: "workers/browser.state",
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*BundleManifest)
	}{
		{"component missing id", func(m *BundleManifest) {
			m.Components = []ComponentDescriptor{{Kind: "task"}}
		}},
		{"component duplicate id", func(m *BundleManifest) {
			m.Components = []ComponentDescriptor{{ID: "task/1"}, {ID: "task/1"}}
		}},
		{"component empty dependency", func(m *BundleManifest) {
			m.Components = []ComponentDescriptor{{ID: "task/1", Depends: []string{""}}}
		}},
		{"filesystem missing id", func(m *BundleManifest) {
			m.Filesystems[0].ID = ""
		}},
		{"filesystem duplicate id", func(m *BundleManifest) {
			m.Filesystems = append(m.Filesystems, m.Filesystems[0])
		}},
		{"filesystem bad source", func(m *BundleManifest) {
			m.Filesystems[0].Source = "../mnt"
		}},
		{"task missing id", func(m *BundleManifest) {
			m.Tasks[0].ID = ""
		}},
		{"task duplicate id", func(m *BundleManifest) {
			m.Tasks = append(m.Tasks, m.Tasks[0])
		}},
		{"task missing kind", func(m *BundleManifest) {
			m.Tasks[0].Kind = ""
		}},
		{"task bad state", func(m *BundleManifest) {
			m.Tasks[0].State = TaskState("mystery")
		}},
		{"namespace task mismatch", func(m *BundleManifest) {
			m.Tasks[0].Namespace.TaskID = "2"
		}},
		{"bind bad destination", func(m *BundleManifest) {
			m.Tasks[0].Namespace.Binds[0].DstPath = "../escape"
		}},
		{"bind bad source", func(m *BundleManifest) {
			m.Tasks[0].Namespace.Binds[0].SrcPath = "../escape"
		}},
		{"bind missing filesystem", func(m *BundleManifest) {
			m.Tasks[0].Namespace.Binds[0].SrcFSID = ""
		}},
		{"bind bad mode", func(m *BundleManifest) {
			m.Tasks[0].Namespace.Binds[0].Mode = "sideways"
		}},
		{"bind negative index", func(m *BundleManifest) {
			m.Tasks[0].Namespace.Binds[0].Index = -1
		}},
		{"bind duplicate index", func(m *BundleManifest) {
			m.Tasks[0].Namespace.Binds = append(m.Tasks[0].Namespace.Binds, BindManifest{
				DstPath: ".",
				SrcFSID: "rootfs",
				SrcPath: ".",
				Mode:    "after",
				Index:   0,
			})
		}},
		{"bind missing index", func(m *BundleManifest) {
			m.Tasks[0].Namespace.Binds = append(m.Tasks[0].Namespace.Binds, BindManifest{
				DstPath: ".",
				SrcFSID: "rootfs",
				SrcPath: ".",
				Mode:    "after",
				Index:   2,
			})
		}},
		{"fd negative", func(m *BundleManifest) {
			m.Tasks[0].FDs[0].FD = -1
		}},
		{"fd duplicate", func(m *BundleManifest) {
			m.Tasks[0].FDs = append(m.Tasks[0].FDs, m.Tasks[0].FDs[0])
		}},
		{"fd missing path", func(m *BundleManifest) {
			m.Tasks[0].FDs[0].Path = ""
		}},
		{"fd bad path", func(m *BundleManifest) {
			m.Tasks[0].FDs[0].Path = "../log"
		}},
		{"vm missing id", func(m *BundleManifest) {
			m.VMs[0].ID = ""
		}},
		{"vm duplicate id", func(m *BundleManifest) {
			m.VMs = append(m.VMs, m.VMs[0])
		}},
		{"vm missing kind", func(m *BundleManifest) {
			m.VMs[0].Kind = ""
		}},
		{"vm bad state path", func(m *BundleManifest) {
			m.VMs[0].StatePath = "../vm.state"
		}},
		{"worker missing id", func(m *BundleManifest) {
			m.Workers[0].ID = ""
		}},
		{"worker duplicate id", func(m *BundleManifest) {
			m.Workers = append(m.Workers, m.Workers[0])
		}},
		{"worker missing kind", func(m *BundleManifest) {
			m.Workers[0].Kind = ""
		}},
		{"worker bad state path", func(m *BundleManifest) {
			m.Workers[0].StatePath = "../worker.state"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := valid()
			tt.mutate(&manifest)
			if err := ValidateBundleManifest(manifest); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ValidateBundleManifest error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestBundleManifestFileRejectsInvalidHeader(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBundleManifest(dir, BundleManifest{Version: BundleManifestVersion}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("WriteBundleManifest invalid header error = %v, want ErrInvalidManifest", err)
	}
	if err := os.WriteFile(filepath.Join(dir, BundleManifestFile), []byte(`{"version":"bad","mode":"migrate"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBundleManifest(dir); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ReadBundleManifest invalid header error = %v, want ErrInvalidManifest", err)
	}
}
