package vm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/migration"
)

func TestDeviceImportManifest(t *testing.T) {
	root, device := newTestDevice(t)
	vm, err := device.ImportManifest(migration.VMManifest{
		ID:   "3",
		Kind: "v86",
		Labels: map[string]string{
			"alias": "guest",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if vm.ID() != "3" || vm.Kind() != "v86" || vm.Alias() != "guest" {
		t.Fatalf("vm = id %q kind %q alias %q", vm.ID(), vm.Kind(), vm.Alias())
	}
	if got, err := device.Lookup("3"); err != nil || got != vm {
		t.Fatalf("Lookup(3) = %v, %v; want imported vm", got, err)
	}
	f, err := fs.OpenContext(context.Background(), root.NS(), "#vm/guest/id")
	if err != nil {
		t.Fatalf("open alias id: %v", err)
	}
	_ = f.Close()

	next, err := device.Alloc("v86")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() != "4" {
		t.Fatalf("next id = %q, want 4", next.ID())
	}
}

func TestDeviceImportManifestWithState(t *testing.T) {
	_, device := newTestDevice(t)
	vm, err := device.ImportManifestWithState(migration.VMManifest{
		ID:        "2",
		Kind:      "v86",
		StatePath: "vm/2.state",
	}, []byte("state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(vm.State()) != "state" {
		t.Fatalf("vm state = %q, want state", vm.State())
	}
	state := vm.State()
	state[0] = 'S'
	if string(vm.State()) != "state" {
		t.Fatalf("vm state changed through caller slice: %q", vm.State())
	}
	if _, err := device.ImportManifestWithState(migration.VMManifest{ID: "3", Kind: "v86"}, []byte("state")); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("missing state path error = %v, want ErrInvalid", err)
	}
	if _, err := device.ImportManifestWithState(migration.VMManifest{ID: "3", Kind: "v86", StatePath: "vm/3.state"}, nil); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("missing state error = %v, want ErrInvalid", err)
	}
}

func TestImportedVMSetGuestInitializesNamespace(t *testing.T) {
	root, device := newTestDevice(t)
	vm, err := device.ImportManifest(migration.VMManifest{ID: "1", Kind: "v86"})
	if err != nil {
		t.Fatal(err)
	}
	guest := memfs.New()
	if err := fs.WriteFile(guest, "ready.txt", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := vm.SetGuest(guest); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		data, err = fs.ReadFile(root.NS(), "#vm/1/guest/ready.txt")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read imported vm guest: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("guest data = %q, want ok", data)
	}
}

func TestDeviceImportManifestRejectsInvalidInput(t *testing.T) {
	_, device := newTestDevice(t)
	if _, err := device.ImportManifest(migration.VMManifest{ID: "bad", Kind: "v86"}); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("bad id error = %v, want ErrInvalid", err)
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "1"}); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("missing kind error = %v, want ErrInvalid", err)
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "1", Kind: "missing"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing kind driver error = %v, want ErrNotExist", err)
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "1", Kind: "v86", StatePath: "vm.state"}); !errors.Is(err, migration.ErrUnsupported) {
		t.Fatalf("state path error = %v, want ErrUnsupported", err)
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "1", Kind: "v86"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "1", Kind: "v86"}); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate id error = %v, want ErrExist", err)
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "2", Kind: "v86", Labels: map[string]string{"alias": "guest"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "3", Kind: "v86", Labels: map[string]string{"alias": "guest"}}); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate alias error = %v, want ErrExist", err)
	}
}

func TestDeviceRemoveImportedManifest(t *testing.T) {
	root, device := newTestDevice(t)
	if _, err := device.ImportManifest(migration.VMManifest{ID: "1", Kind: "v86", Labels: map[string]string{"alias": "guest"}}); err != nil {
		t.Fatal(err)
	}
	device.Remove("1")
	if _, err := device.Lookup("1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lookup removed vm error = %v, want ErrNotExist", err)
	}
	if _, err := fs.OpenContext(context.Background(), root.NS(), "#vm/guest/id"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open removed alias error = %v, want ErrNotExist", err)
	}
}

func TestDeviceRemoveRewindsNextID(t *testing.T) {
	_, device := newTestDevice(t)
	first, err := device.Alloc("v86")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != "1" {
		t.Fatalf("first id = %q, want 1", first.ID())
	}
	if _, err := device.ImportManifest(migration.VMManifest{ID: "5", Kind: "v86"}); err != nil {
		t.Fatal(err)
	}
	device.Remove("5")
	next, err := device.Alloc("v86")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() != "2" {
		t.Fatalf("next id = %q, want 2", next.ID())
	}
}

func TestDeviceAllocConcurrent(t *testing.T) {
	_, device := newTestDevice(t)
	const n = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	errc := make(chan error, n)
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := device.Alloc("v86")
			if err != nil {
				errc <- err
				return
			}
			ids <- r.ID()
		}()
	}
	close(start)
	wg.Wait()
	close(errc)
	close(ids)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d ids, want %d", len(seen), n)
	}
}

func newTestDevice(t *testing.T) (*wanix.Task, *Device) {
	t.Helper()
	root, err := wanix.NewRoot()
	if err != nil {
		t.Fatal(err)
	}
	device := New(root)
	if err := root.NS().Bind(device, ".", "#vm"); err != nil {
		t.Fatal(err)
	}
	if err := root.NS().Bind(memfs.New(), ".", "#vm/v86"); err != nil {
		t.Fatal(err)
	}
	return root, device
}
