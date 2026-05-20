//go:build js && wasm

// Package llm exposes browser-hosted language model APIs as files.
package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sync"
	"syscall/js"
	"time"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/misc/jsutil"
)

// FS exposes the browser Prompt API.
type FS struct{}

// New returns a filesystem backed by the browser Prompt API.
func New() *FS {
	return &FS{}
}

var _ fs.FS = (*FS)(nil)

// Open opens the named file.
func (fsys *FS) Open(name string) (fs.File, error) {
	return fsys.OpenContext(context.Background(), name)
}

// OpenContext opens the named file.
func (fsys *FS) OpenContext(ctx context.Context, name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	switch name {
	case ".":
		return fskit.DirFile(fskit.Entry(".", fs.ModeDir|0755),
			fskit.Entry("availability", 0444),
			fskit.Entry("download", 0777),
			fskit.Entry("prompt", 0777),
		), nil
	case "availability":
		return &fskit.FuncFile{
			Node: fskit.Entry("availability", 0444),
			ReadFunc: func(n *fskit.Node) error {
				availability, err := promptAvailability()
				if err != nil {
					fskit.SetData(n, []byte("error: "+err.Error()+"\n"))
					return nil
				}
				fskit.SetData(n, []byte(availability+"\n"))
				return nil
			},
		}, nil
	case "download":
		return newDownloadFile(name), nil
	case "prompt":
		return newPromptFile(name), nil
	default:
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
}

type downloadFile struct {
	name string

	mu      sync.Mutex
	pending []byte
	off     int
	closed  bool
	eof     bool
	ran     bool
}

func newDownloadFile(name string) *downloadFile {
	return &downloadFile{name: name}
}

func (f *downloadFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(path.Base(f.name), 0777), nil
}

func (f *downloadFile) Read(b []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, fs.ErrClosed
	}
	if f.eof {
		f.eof = false
		f.mu.Unlock()
		return 0, io.EOF
	}
	if len(f.pending) > f.off {
		n := copy(b, f.pending[f.off:])
		f.off += n
		if f.off >= len(f.pending) {
			f.pending = nil
			f.off = 0
			f.eof = true
		}
		f.mu.Unlock()
		return n, nil
	}
	if f.ran {
		f.mu.Unlock()
		return 0, io.EOF
	}
	f.ran = true
	f.mu.Unlock()

	f.mu.Lock()
	f.pending = []byte(downloadOnce() + "\n")
	f.off = 0
	f.mu.Unlock()
	return f.Read(b)
}

func (f *downloadFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, fs.ErrClosed
	}
	f.ran = true
	f.pending = []byte(downloadOnce() + "\n")
	f.off = 0
	f.eof = false
	f.mu.Unlock()
	return len(p), nil
}

func (f *downloadFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *downloadFile) Seek(int64, int) (int64, error) {
	return 0, &fs.PathError{Op: "seek", Path: f.name, Err: fs.ErrInvalid}
}

type promptOutcome struct {
	data []byte
	err  error
}

type promptFile struct {
	name string

	mu      sync.Mutex
	lineBuf []byte
	pending []byte
	off     int
	closed  bool

	results chan promptOutcome
	eof     bool
}

func newPromptFile(name string) *promptFile {
	return &promptFile{
		name:    name,
		results: make(chan promptOutcome, 16),
	}
}

func (f *promptFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(path.Base(f.name), 0777), nil
}

func (f *promptFile) Read(b []byte) (int, error) {
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, fs.ErrClosed
		}
		if f.eof {
			f.eof = false
			f.mu.Unlock()
			return 0, io.EOF
		}
		if len(f.pending) > f.off {
			n := copy(b, f.pending[f.off:])
			f.off += n
			if f.off >= len(f.pending) {
				f.pending = nil
				f.off = 0
				f.eof = true
			}
			f.mu.Unlock()
			return n, nil
		}
		f.mu.Unlock()

		out, ok := <-f.results
		if !ok {
			return 0, io.EOF
		}
		if out.err != nil {
			return 0, out.err
		}
		f.mu.Lock()
		f.pending = out.data
		f.off = 0
		if len(f.pending) == 0 {
			f.eof = true
		}
		f.mu.Unlock()
	}
}

func (f *promptFile) Write(b []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, fs.ErrClosed
	}
	f.lineBuf = append(f.lineBuf, b...)
	var prompts []string
	for {
		i := bytes.IndexByte(f.lineBuf, '\n')
		if i < 0 {
			break
		}
		line := append([]byte(nil), f.lineBuf[:i]...)
		f.lineBuf = f.lineBuf[i+1:]
		prompts = append(prompts, string(line))
	}
	f.mu.Unlock()

	for _, prompt := range prompts {
		f.results <- promptOutcome{data: []byte(promptOnce(prompt) + "\n")}
	}
	return len(b), nil
}

func (f *promptFile) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	prompt := string(f.lineBuf)
	f.lineBuf = nil
	f.closed = true
	f.mu.Unlock()

	if prompt != "" {
		f.results <- promptOutcome{data: []byte(promptOnce(prompt) + "\n")}
	}
	close(f.results)
	return nil
}

func (f *promptFile) Seek(int64, int) (int64, error) {
	return 0, &fs.PathError{Op: "seek", Path: f.name, Err: fs.ErrInvalid}
}

func promptAPI() (js.Value, error) {
	api := js.Global().Get("LanguageModel")
	if api.IsUndefined() || api.IsNull() {
		return js.Undefined(), errors.New("prompt api unavailable")
	}
	return api, nil
}

func promptAvailability() (string, error) {
	api, err := promptAPI()
	if err != nil {
		return "unavailable", nil
	}
	availability, err := awaitErr(api.Call("availability", languageModelOptions()), 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("llm availability: %w", err)
	}
	return availability.String(), nil
}

func downloadOnce() string {
	status, err := ensureModel()
	if err != nil {
		return "error: " + err.Error()
	}
	return status
}

func ensureModel() (string, error) {
	api, err := promptAPI()
	if err != nil {
		return "", err
	}
	availability, err := promptAvailability()
	if err != nil {
		return "", err
	}
	switch availability {
	case "available":
		return "available", nil
	case "downloadable", "downloading":
	default:
		return availability, nil
	}
	session, err := awaitErr(api.Call("create", languageModelOptions()), 5*time.Minute)
	if err != nil {
		return "", fmt.Errorf("llm download: %w", err)
	}
	destroySession(session)
	availability, err = promptAvailability()
	if err != nil {
		return "", err
	}
	return availability, nil
}

func promptOnce(prompt string) string {
	response, err := runPrompt(prompt)
	if err != nil {
		return "error: " + err.Error()
	}
	return response
}

func runPrompt(prompt string) (string, error) {
	api, err := promptAPI()
	if err != nil {
		return "", err
	}
	availability, err := promptAvailability()
	if err != nil {
		return "", err
	}
	if availability != "available" {
		return "", fmt.Errorf("llm unavailable: %s", availability)
	}
	session, err := awaitErr(api.Call("create", languageModelOptions()), 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("llm create: %w", err)
	}
	defer destroySession(session)

	response, err := awaitErr(session.Call("prompt", prompt), 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("llm prompt: %w", err)
	}
	return response.String(), nil
}

func destroySession(session js.Value) {
	destroy := session.Get("destroy")
	if destroy.IsUndefined() || destroy.IsNull() {
		return
	}
	destroy.Invoke()
}

func languageModelOptions() js.Value {
	languages := js.Global().Get("Array").New(1)
	languages.SetIndex(0, "en")

	input := js.Global().Get("Object").New()
	input.Set("type", "text")
	input.Set("languages", languages)

	output := js.Global().Get("Object").New()
	output.Set("type", "text")
	output.Set("languages", languages)

	inputs := js.Global().Get("Array").New(1)
	inputs.SetIndex(0, input)
	outputs := js.Global().Get("Array").New(1)
	outputs.SetIndex(0, output)

	opts := js.Global().Get("Object").New()
	opts.Set("expectedInputs", inputs)
	opts.Set("expectedOutputs", outputs)
	return opts
}

func awaitErr(promise js.Value, timeout time.Duration) (js.Value, error) {
	type result struct {
		value js.Value
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		value, err := jsutil.AwaitErr(promise)
		ch <- result{value: value, err: err}
	}()
	select {
	case r := <-ch:
		return r.value, r.err
	case <-time.After(timeout):
		return js.Undefined(), fmt.Errorf("timeout")
	}
}
