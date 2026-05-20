package fs

import (
	"errors"
	"io/fs"
	"testing"
)

func Test_OpErr(t *testing.T) {
	e := opErr(nil, "test", "test", ErrNotSupported)
	if !errors.Is(e, ErrNotSupported) {
		t.Errorf("expected ErrNotSupported, got %v", e)
	}
}

func TestEqualUsesComparableIdentity(t *testing.T) {
	a := &equalTestFS{name: "same"}
	b := &equalTestFS{name: "same"}
	if !Equal(a, a) {
		t.Fatal("same filesystem pointer is not equal to itself")
	}
	if Equal(a, b) {
		t.Fatal("distinct filesystem pointers compared equal")
	}
}

type equalTestFS struct {
	name string
}

func (*equalTestFS) Open(string) (fs.File, error) {
	return nil, fs.ErrNotExist
}
