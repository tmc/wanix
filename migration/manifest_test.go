package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBundleManifestRoundTrip(t *testing.T) {
	created := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	in := BundleManifest{
		Version:   "wanix-migration-v1",
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
			ID:      "1",
			Kind:    "gojs",
			Alias:   "shell",
			Command: "rc",
			FDs: []FDManifest{{
				FD:         3,
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
	if got := out.Tasks[0].Alias; got != "shell" {
		t.Fatalf("task alias = %q, want shell", got)
	}
	if got := out.Filesystems[0].OverlayFSID; got != "overlayfs" {
		t.Fatalf("filesystem overlay id = %q, want overlayfs", got)
	}
	if got := out.Tasks[0].Namespace.Binds[0].SrcFSID; got != "rootfs" {
		t.Fatalf("namespace bind src fs id = %q, want rootfs", got)
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
