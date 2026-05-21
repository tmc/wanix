//go:build js && wasm

// Package hostfs exposes a host-provided namespace to Wanix.
package hostfs

import (
	"bytes"
	"io"
	"os"
	"path"
	"strings"
	"syscall/js"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
)

// FS delegates file operations to a JavaScript object.
type FS struct {
	v    js.Value
	name string
}

// New returns a host filesystem rooted at v.
func New(v js.Value) *FS {
	return &FS{v: v}
}

// NewGlobal returns a host filesystem looked up by global variable name.
func NewGlobal(name string) *FS {
	return &FS{name: name}
}

func (f *FS) value() js.Value {
	if f.name != "" {
		return js.Global().Get(f.name)
	}
	return f.v
}

func (f *FS) available() bool {
	v := f.value()
	return !v.IsUndefined() && !v.IsNull()
}

// Open opens name for reading.
func (f *FS) Open(name string) (fs.File, error) {
	if !f.available() {
		if name == "." {
			return fskit.DirFile(fskit.Entry(".", fs.ModeDir|0555)), nil
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if f.isDir(name) {
		ents, err := f.ReadDir(name)
		if err != nil {
			return nil, err
		}
		return fskit.DirFile(fskit.Entry(path.Base(name), fs.ModeDir|0555), ents...), nil
	}
	data, err := f.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return &hostFile{name: name, data: data}, nil
}

// OpenFile opens name for shell redirection.
func (f *FS) OpenFile(name string, flag int, perm fs.FileMode) (fs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		return f.Open(name)
	}
	return &hostFile{name: name, fsys: f, writable: true}, nil
}

// ReadDir lists a host directory.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	v, err := call(f.value(), "readDir", name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	ents := make([]fs.DirEntry, 0, v.Length())
	for i := 0; i < v.Length(); i++ {
		item := v.Index(i)
		if item.Type() == js.TypeString {
			ents = append(ents, fskit.Entry(item.String(), 0644))
			continue
		}
		name := item.Get("name").String()
		mode := fs.FileMode(0644)
		if item.Get("dir").Bool() {
			mode = fs.ModeDir | 0555
		}
		ents = append(ents, fskit.Entry(name, mode))
	}
	return ents, nil
}

// ReadFile reads a host file.
func (f *FS) ReadFile(name string) ([]byte, error) {
	v, err := call(f.value(), "readFile", name)
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	return []byte(v.String()), nil
}

// WriteFile writes a host file.
func (f *FS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	_, err := call(f.value(), "writeFile", name, string(data))
	if err != nil {
		return &fs.PathError{Op: "write", Path: name, Err: err}
	}
	return nil
}

func (f *FS) isDir(name string) bool {
	v, err := call(f.value(), "isDir", name)
	return err == nil && v.Bool()
}

type hostFile struct {
	name     string
	fsys     *FS
	data     []byte
	off      int64
	writable bool
	buf      bytes.Buffer
}

func (f *hostFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(path.Base(f.name), 0644, int64(len(f.data))), nil
}

func (f *hostFile) Read(p []byte) (int, error) {
	if f.off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += int64(n)
	return n, nil
}

func (f *hostFile) Write(p []byte) (int, error) {
	if !f.writable {
		return 0, fs.ErrPermission
	}
	return f.buf.Write(p)
}

func (f *hostFile) Close() error {
	if f.writable {
		return f.fsys.WriteFile(f.name, f.buf.Bytes(), 0644)
	}
	return nil
}

func (f *hostFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.off + offset
	case io.SeekEnd:
		next = int64(len(f.data)) + offset
	default:
		return 0, fs.ErrInvalid
	}
	if next < 0 {
		return 0, fs.ErrInvalid
	}
	f.off = next
	return f.off, nil
}

func call(v js.Value, method, name string, args ...any) (out js.Value, err error) {
	defer func() {
		if recover() != nil {
			err = fs.ErrInvalid
		}
	}()
	fn := v.Get(method)
	if fn.IsUndefined() || fn.IsNull() {
		return js.Undefined(), fs.ErrNotExist
	}
	name = strings.Trim(name, "/")
	if name == "." {
		name = ""
	}
	jsargs := []any{name}
	jsargs = append(jsargs, args...)
	return fn.Invoke(jsargs...), nil
}

var _ fs.FS = (*FS)(nil)
var _ fs.OpenFileFS = (*FS)(nil)
var _ fs.ReadDirFS = (*FS)(nil)
var _ fs.ReadFileFS = (*FS)(nil)
var _ fs.WriteFileFS = (*FS)(nil)
