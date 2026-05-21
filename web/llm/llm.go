//go:build js && wasm

// Package llm exposes browser-hosted language model APIs as files.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
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
var _ fs.OpenFileFS = (*FS)(nil)

// Open opens the named file.
func (fsys *FS) Open(name string) (fs.File, error) {
	return fsys.OpenContext(context.Background(), name)
}

// OpenFile opens name with flags. Control files ignore create and truncate flags
// so shell redirections such as "echo download > llm/ctl" work as commands.
func (fsys *FS) OpenFile(name string, flag int, perm fs.FileMode) (fs.File, error) {
	if name == "ctl" && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return newCtlFile(name), nil
	}
	if strings.HasSuffix(name, "/ctl") && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return openSessionPath(name)
	}
	if strings.HasSuffix(name, "/prompt") && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return openSessionPath(name)
	}
	if strings.HasSuffix(name, "/prefill") && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return openSessionPath(name)
	}
	if strings.HasSuffix(name, "/schema") && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return openSessionPath(name)
	}
	if strings.HasSuffix(name, "/system") && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return openSessionPath(name)
	}
	return fsys.Open(name)
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
			fskit.Entry("chat", fs.ModeDir|0755),
			fskit.Entry("ctl", 0222),
			fskit.Entry("new", 0444),
			fskit.Entry("prompt", 0777),
			fskit.Entry("status", 0444),
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
	case "ctl":
		return newCtlFile(name), nil
	case "new":
		return newSessionFile(name), nil
	case "prompt":
		return newPromptFile(name), nil
	case "chat":
		return fskit.DirFile(fskit.Entry("chat", fs.ModeDir|0755),
			fskit.Entry("input", 0222),
			fskit.Entry("output", 0444),
			fskit.Entry("status", 0444),
		), nil
	case "chat/input":
		return newPromptFile(name), nil
	case "chat/output":
		return newPromptFile(name), nil
	case "chat/status":
		return statusFile(name), nil
	case "status":
		return statusFile(name), nil
	default:
		return openSessionPath(name)
	}
}

func statusFile(name string) fs.File {
	return &fskit.FuncFile{
		Node: fskit.Entry(path.Base(name), 0444),
		ReadFunc: func(n *fskit.Node) error {
			status, err := promptStatus()
			if err != nil {
				fskit.SetData(n, []byte(`{"error":`+quoteJSON(err.Error())+"}\n"))
				return nil
			}
			data, err := json.MarshalIndent(status, "", "  ")
			if err != nil {
				return err
			}
			fskit.SetData(n, append(data, '\n'))
			return nil
		},
	}
}

type Status struct {
	API          string         `json:"api"`
	Availability string         `json:"availability"`
	Download     DownloadStatus `json:"download"`
	Error        string         `json:"error,omitempty"`
	FS           string         `json:"fs"`
	UserAgent    string         `json:"user_agent"`
	Chrome       string         `json:"chrome,omitempty"`
}

type DownloadStatus struct {
	State   string  `json:"state"`
	Loaded  float64 `json:"loaded,omitempty"`
	Message string  `json:"message,omitempty"`
	Started string  `json:"started,omitempty"`
	Elapsed float64 `json:"elapsed_seconds,omitempty"`
}

var downloadState = struct {
	mu       sync.Mutex
	status   DownloadStatus
	started  time.Time
	inFlight bool
}{
	status: DownloadStatus{State: "idle"},
}

func setDownloadStatus(status DownloadStatus) {
	downloadState.mu.Lock()
	defer downloadState.mu.Unlock()
	if status.State == "starting" || status.State == "downloading" {
		if downloadState.started.IsZero() {
			downloadState.started = time.Now()
		}
		status.Started = downloadState.started.Format(time.RFC3339)
		status.Elapsed = time.Since(downloadState.started).Seconds()
	} else {
		downloadState.started = time.Time{}
	}
	downloadState.status = status
}

func currentDownloadStatus() DownloadStatus {
	downloadState.mu.Lock()
	defer downloadState.mu.Unlock()
	status := downloadState.status
	if !downloadState.started.IsZero() {
		status.Started = downloadState.started.Format(time.RFC3339)
		status.Elapsed = time.Since(downloadState.started).Seconds()
	}
	return status
}

var sessions = struct {
	mu   sync.Mutex
	next int
	byID map[string]*session
}{
	byID: make(map[string]*session),
}

func newSession() *session {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.next++
	id := strconv.Itoa(sessions.next)
	s := &session{
		id:         id,
		created:    time.Now(),
		controller: js.Undefined(),
		output:     make(chan promptOutcome, 64),
		state:      "idle",
	}
	sessions.byID[id] = s
	return s
}

func cloneSession(src *session) *session {
	src.mu.Lock()
	system := src.system
	prefill := src.prefill
	schema := src.schema
	history := append([]promptMessage(nil), src.history...)
	lastUser := src.lastUser
	lastAnswer := src.lastAnswer
	src.mu.Unlock()
	s := newSession()
	s.mu.Lock()
	s.system = system
	s.prefill = prefill
	s.schema = schema
	s.history = history
	s.lastUser = lastUser
	s.lastAnswer = lastAnswer
	s.mu.Unlock()
	return s
}

func getSession(id string) (*session, bool) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	s, ok := sessions.byID[id]
	return s, ok
}

func removeSession(id string) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	delete(sessions.byID, id)
}

type session struct {
	mu          sync.Mutex
	id          string
	created     time.Time
	state       string
	err         string
	system      string
	prefill     string
	schema      string
	lastUser    string
	lastAnswer  string
	history     []promptMessage
	contextWin  int
	contextUse  int
	controller  js.Value
	controllerF func()
	output      chan promptOutcome
	closed      bool
	wg          sync.WaitGroup
}

func (s *session) start(prompt string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fs.ErrClosed
	}
	s.state = "running"
	s.err = ""
	s.lastUser = prompt
	s.lastAnswer = ""
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		if err := s.run(prompt); err != nil {
			s.mu.Lock()
			s.state = "error"
			s.err = err.Error()
			s.mu.Unlock()
			s.output <- promptOutcome{data: []byte("error: " + err.Error() + "\n")}
			return
		}
		s.mu.Lock()
		if !s.closed {
			s.state = "done"
		}
		s.mu.Unlock()
		s.output <- promptOutcome{data: []byte("\n")}
		s.output <- promptOutcome{eof: true}
	}()
	return nil
}

func (s *session) run(prompt string) error {
	s.mu.Lock()
	system := s.system
	prefill := s.prefill
	schema := s.schema
	history := append([]promptMessage(nil), s.history...)
	s.mu.Unlock()
	var answer strings.Builder
	err := runPromptStream(prompt, s.output, streamOptions{
		system:  system,
		prefill: prefill,
		schema:  schema,
		history: history,
		answer:  &answer,
		session: s,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.lastAnswer = answer.String()
	s.history = append(s.history,
		promptMessage{role: "user", content: prompt},
		promptMessage{role: "assistant", content: s.lastAnswer},
	)
	s.mu.Unlock()
	return nil
}

func (s *session) stop() {
	s.mu.Lock()
	controller := s.controller
	s.mu.Unlock()
	if controller.Truthy() {
		controller.Call("abort")
	}
}

func (s *session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.state = "closed"
	controller := s.controller
	s.mu.Unlock()
	if controller.Truthy() {
		controller.Call("abort")
	}
	s.wg.Wait()
	close(s.output)
	removeSession(s.id)
}

func (s *session) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := map[string]any{
		"id":              s.id,
		"state":           s.state,
		"created":         s.created.Format(time.RFC3339),
		"elapsed_seconds": time.Since(s.created).Seconds(),
	}
	if s.system != "" {
		status["system"] = true
	}
	if s.prefill != "" {
		status["prefill"] = true
	}
	if s.schema != "" {
		status["schema"] = true
	}
	if len(s.history) != 0 {
		status["turns"] = len(s.history) / 2
	}
	if s.lastUser != "" {
		status["last_user"] = s.lastUser
	}
	if s.contextWin != 0 {
		status["context_window"] = s.contextWin
		status["context_usage"] = s.contextUse
		status["context_left"] = s.contextWin - s.contextUse
	}
	if s.err != "" {
		status["error"] = s.err
	}
	return status
}

func openSessionPath(name string) (fs.File, error) {
	id, elem, ok := strings.Cut(name, "/")
	if !ok {
		if s, exists := getSession(id); exists {
			return sessionDir(s), nil
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	s, exists := getSession(id)
	if !exists {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	switch elem {
	case "clone":
		return sessionCloneFile(s), nil
	case "ctl":
		return &sessionCtlFile{name: path.Base(elem), session: s}, nil
	case "prompt":
		return &sessionPromptFile{name: path.Base(elem), session: s}, nil
	case "output":
		return &sessionOutputFile{name: path.Base(elem), session: s}, nil
	case "prefill":
		return &sessionTextFile{name: path.Base(elem), session: s, field: "prefill"}, nil
	case "schema":
		return &sessionTextFile{name: path.Base(elem), session: s, field: "schema"}, nil
	case "status":
		return sessionStatusFile(s), nil
	case "system":
		return &sessionTextFile{name: path.Base(elem), session: s, field: "system"}, nil
	default:
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
}

func sessionDir(*session) fs.File {
	return fskit.DirFile(fskit.Entry(".", fs.ModeDir|0755),
		fskit.Entry("clone", 0444),
		fskit.Entry("ctl", 0222),
		fskit.Entry("output", 0444),
		fskit.Entry("prefill", 0666),
		fskit.Entry("prompt", 0222),
		fskit.Entry("schema", 0666),
		fskit.Entry("status", 0444),
		fskit.Entry("system", 0666),
	)
}

func newSessionFile(name string) fs.File {
	return &fskit.FuncFile{
		Node: fskit.Entry(path.Base(name), 0444),
		ReadFunc: func(n *fskit.Node) error {
			s := newSession()
			fskit.SetData(n, []byte(s.id+"\n"))
			return nil
		},
	}
}

func sessionStatusFile(s *session) fs.File {
	return &fskit.FuncFile{
		Node: fskit.Entry("status", 0444),
		ReadFunc: func(n *fskit.Node) error {
			data, err := json.MarshalIndent(s.status(), "", "  ")
			if err != nil {
				return err
			}
			fskit.SetData(n, append(data, '\n'))
			return nil
		},
	}
}

func sessionCloneFile(s *session) fs.File {
	return &fskit.FuncFile{
		Node: fskit.Entry("clone", 0444),
		ReadFunc: func(n *fskit.Node) error {
			clone := cloneSession(s)
			fskit.SetData(n, []byte(clone.id+"\n"))
			return nil
		},
	}
}

type sessionCtlFile struct {
	name    string
	session *session
}

func (f *sessionCtlFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(f.name, 0222), nil
}

func (f *sessionCtlFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: f.name, Err: fs.ErrInvalid}
}

func (f *sessionCtlFile) Write(p []byte) (int, error) {
	switch string(bytes.TrimSpace(p)) {
	case "close":
		f.session.close()
	case "continue":
		if err := f.session.start("Continue."); err != nil {
			return 0, err
		}
	case "stop":
		f.session.stop()
	default:
		return 0, &fs.PathError{Op: "write", Path: f.name, Err: fs.ErrInvalid}
	}
	return len(p), nil
}

func (f *sessionCtlFile) Close() error {
	return nil
}

type sessionTextFile struct {
	name    string
	session *session
	field   string
	buf     []byte
	off     int
}

func (f *sessionTextFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(f.name, 0666), nil
}

func (f *sessionTextFile) Read(b []byte) (int, error) {
	f.session.mu.Lock()
	text := f.textLocked()
	f.session.mu.Unlock()
	if f.off >= len(text) {
		return 0, io.EOF
	}
	n := copy(b, text[f.off:])
	f.off += n
	return n, nil
}

func (f *sessionTextFile) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	return len(p), nil
}

func (f *sessionTextFile) Close() error {
	if f.buf == nil {
		return nil
	}
	text := string(f.buf)
	f.session.mu.Lock()
	switch f.field {
	case "prefill":
		f.session.prefill = text
	case "schema":
		f.session.schema = text
	case "system":
		f.session.system = text
	}
	f.session.mu.Unlock()
	return nil
}

func (f *sessionTextFile) Truncate(int64) error {
	f.buf = nil
	f.session.mu.Lock()
	switch f.field {
	case "prefill":
		f.session.prefill = ""
	case "schema":
		f.session.schema = ""
	case "system":
		f.session.system = ""
	}
	f.session.mu.Unlock()
	return nil
}

func (f *sessionTextFile) textLocked() []byte {
	var text string
	switch f.field {
	case "prefill":
		text = f.session.prefill
	case "schema":
		text = f.session.schema
	case "system":
		text = f.session.system
	}
	return []byte(text)
}

type sessionPromptFile struct {
	name    string
	session *session
	lineBuf []byte
}

func (f *sessionPromptFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(f.name, 0222), nil
}

func (f *sessionPromptFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: f.name, Err: fs.ErrInvalid}
}

func (f *sessionPromptFile) Write(p []byte) (int, error) {
	f.lineBuf = append(f.lineBuf, p...)
	for {
		i := bytes.IndexByte(f.lineBuf, '\n')
		if i < 0 {
			break
		}
		line := append([]byte(nil), f.lineBuf[:i]...)
		f.lineBuf = f.lineBuf[i+1:]
		if err := f.session.start(string(line)); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (f *sessionPromptFile) Close() error {
	if len(f.lineBuf) == 0 {
		return nil
	}
	prompt := string(f.lineBuf)
	f.lineBuf = nil
	return f.session.start(prompt)
}

type sessionOutputFile struct {
	name    string
	session *session
	pending []byte
	off     int
}

func (f *sessionOutputFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(f.name, 0444), nil
}

func (f *sessionOutputFile) Read(b []byte) (int, error) {
	for {
		if len(f.pending) > f.off {
			n := copy(b, f.pending[f.off:])
			f.off += n
			if f.off >= len(f.pending) {
				f.pending = nil
				f.off = 0
			}
			return n, nil
		}
		out, ok := <-f.session.output
		if !ok {
			return 0, io.EOF
		}
		if out.eof {
			return 0, io.EOF
		}
		if out.err != nil {
			return 0, out.err
		}
		f.pending = out.data
		f.off = 0
	}
}

func (f *sessionOutputFile) Close() error {
	return nil
}

func beginDownload() bool {
	downloadState.mu.Lock()
	defer downloadState.mu.Unlock()
	if downloadState.inFlight {
		return false
	}
	downloadState.inFlight = true
	return true
}

func endDownload() {
	downloadState.mu.Lock()
	defer downloadState.mu.Unlock()
	downloadState.inFlight = false
}

type ctlFile struct {
	name string

	mu     sync.Mutex
	closed bool
}

func newCtlFile(name string) *ctlFile {
	return &ctlFile{name: name}
}

func (f *ctlFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry(path.Base(f.name), 0222), nil
}

func (f *ctlFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: f.name, Err: fs.ErrInvalid}
}

func (f *ctlFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, fs.ErrClosed
	}
	f.mu.Unlock()

	switch string(bytes.TrimSpace(p)) {
	case "download", "start":
		if !beginDownload() {
			setDownloadStatus(DownloadStatus{State: "downloading", Message: "already running"})
			return len(p), nil
		}
		go func() {
			defer endDownload()
			if _, err := ensureModel(); err != nil {
				setDownloadStatus(DownloadStatus{State: "error", Message: err.Error()})
			}
		}()
	default:
		return 0, &fs.PathError{Op: "write", Path: f.name, Err: fs.ErrInvalid}
	}
	return len(p), nil
}

func (f *ctlFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *ctlFile) Seek(int64, int) (int64, error) {
	return 0, &fs.PathError{Op: "seek", Path: f.name, Err: fs.ErrInvalid}
}

func (f *ctlFile) Truncate(int64) error {
	return nil
}

type promptOutcome struct {
	data []byte
	err  error
	eof  bool
}

type promptFile struct {
	name string

	mu      sync.Mutex
	lineBuf []byte
	pending []byte
	off     int
	closed  bool

	results chan promptOutcome
	wg      sync.WaitGroup
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
			}
			f.mu.Unlock()
			return n, nil
		}
		f.mu.Unlock()

		out, ok := <-f.results
		if !ok {
			return 0, io.EOF
		}
		if out.eof {
			return 0, io.EOF
		}
		if out.err != nil {
			return 0, out.err
		}
		f.mu.Lock()
		f.pending = out.data
		f.off = 0
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

	f.startPromptStreams(prompts)
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

	var prompts []string
	if prompt != "" {
		prompts = append(prompts, prompt)
	}
	f.startPromptStreams(prompts)
	f.wg.Wait()
	close(f.results)
	return nil
}

func (f *promptFile) Seek(int64, int) (int64, error) {
	return 0, &fs.PathError{Op: "seek", Path: f.name, Err: fs.ErrInvalid}
}

func (f *promptFile) startPromptStreams(prompts []string) {
	for _, prompt := range prompts {
		f.wg.Add(1)
		go func(prompt string) {
			defer f.wg.Done()
			streamPrompt(prompt, f.results)
		}(prompt)
	}
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

func promptStatus() (Status, error) {
	status := Status{
		API:       "missing",
		Download:  currentDownloadStatus(),
		FS:        "#web/llm",
		UserAgent: js.Global().Get("navigator").Get("userAgent").String(),
		Chrome:    chromeBrands(),
	}
	if _, err := promptAPI(); err != nil {
		status.Availability = "unavailable"
		status.Error = err.Error()
		return status, nil
	}
	status.API = "LanguageModel"
	availability, err := promptAvailability()
	if err != nil {
		status.Availability = "error"
		status.Error = err.Error()
		return status, nil
	}
	status.Availability = availability
	if availability == "downloading" && status.Download.State == "idle" {
		status.Download = DownloadStatus{State: "downloading"}
	}
	return status, nil
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
		setDownloadStatus(DownloadStatus{State: "available", Loaded: 1})
		return "available", nil
	case "downloadable":
	case "downloading":
		setDownloadStatus(DownloadStatus{State: "downloading"})
	default:
		setDownloadStatus(DownloadStatus{State: availability})
		return availability, nil
	}
	setDownloadStatus(DownloadStatus{State: "starting"})
	opts, release := languageModelOptionsWithMonitor()
	defer release()
	session, err := awaitErr(api.Call("create", opts), 24*time.Hour)
	if err != nil {
		setDownloadStatus(DownloadStatus{State: "error", Message: err.Error()})
		return "", fmt.Errorf("llm download: %w", err)
	}
	destroySession(session)
	availability, err = promptAvailability()
	if err != nil {
		setDownloadStatus(DownloadStatus{State: "error", Message: err.Error()})
		return "", err
	}
	if availability == "available" {
		setDownloadStatus(DownloadStatus{State: "available", Loaded: 1})
	} else {
		setDownloadStatus(DownloadStatus{State: availability})
	}
	return availability, nil
}

func chromeBrands() string {
	brands := js.Global().Get("navigator").Get("userAgentData").Get("brands")
	if brands.IsUndefined() || brands.IsNull() {
		return ""
	}
	data, err := jsonStringify(brands)
	if err != nil {
		return ""
	}
	return data
}

func jsonStringify(v js.Value) (string, error) {
	result, err := awaitErr(js.Global().Get("Promise").Call("resolve", js.Global().Get("JSON").Call("stringify", v)), time.Second)
	if err != nil {
		return "", err
	}
	return result.String(), nil
}

func quoteJSON(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(data)
}

func promptOnce(prompt string) string {
	response, err := runPrompt(prompt)
	if err != nil {
		return "error: " + err.Error()
	}
	return response
}

func streamPrompt(prompt string, out chan<- promptOutcome) {
	if err := runPromptStream(prompt, out, streamOptions{}); err != nil {
		out <- promptOutcome{data: []byte("error: " + err.Error() + "\n")}
		return
	}
	out <- promptOutcome{data: []byte("\n")}
	out <- promptOutcome{eof: true}
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

type streamOptions struct {
	system  string
	prefill string
	schema  string
	history []promptMessage
	answer  *strings.Builder
	session *session
}

type promptMessage struct {
	role    string
	content string
	prefix  bool
}

func runPromptStream(prompt string, out chan<- promptOutcome, opts streamOptions) error {
	api, err := promptAPI()
	if err != nil {
		return err
	}
	availability, err := promptAvailability()
	if err != nil {
		return err
	}
	if availability != "available" {
		return fmt.Errorf("llm unavailable: %s", availability)
	}
	createOpts := languageModelOptions()
	if opts.system != "" || len(opts.history) != 0 {
		createOpts.Set("initialPrompts", initialPrompts(opts.system, opts.history))
	}
	session, err := awaitErr(api.Call("create", createOpts), 30*time.Second)
	if err != nil {
		return fmt.Errorf("llm create: %w", err)
	}
	defer destroySession(session)
	updateSessionContext(opts.session, session)

	promptOpts, release, err := promptCallOptions(opts)
	if err != nil {
		return err
	}
	defer release()

	promptStreaming := session.Get("promptStreaming")
	promptValue := promptArgument(prompt, opts.prefill)
	if promptStreaming.IsUndefined() || promptStreaming.IsNull() {
		response, err := awaitErr(session.Call("prompt", promptValue, promptOpts), 2*time.Minute)
		if err != nil {
			return fmt.Errorf("llm prompt: %w", err)
		}
		out <- promptOutcome{data: []byte(response.String())}
		if opts.answer != nil {
			opts.answer.WriteString(response.String())
		}
		updateSessionContext(opts.session, session)
		return nil
	}
	stream := session.Call("promptStreaming", promptValue, promptOpts)
	reader := stream.Call("getReader")
	defer reader.Call("releaseLock")
	for {
		result, err := awaitErr(reader.Call("read"), 2*time.Minute)
		if err != nil {
			return fmt.Errorf("llm prompt stream: %w", err)
		}
		if result.Get("done").Bool() {
			return nil
		}
		chunk := streamChunkBytes(result.Get("value"))
		if len(chunk) > 0 {
			if opts.answer != nil {
				opts.answer.Write(chunk)
			}
			out <- promptOutcome{data: chunk}
		}
	}
}

func initialPrompts(system string, history []promptMessage) js.Value {
	prompts := js.Global().Get("Array").New()
	if system != "" {
		prompts.Call("push", promptMessageValue(promptMessage{
			role:    "system",
			content: system,
		}))
	}
	for _, msg := range history {
		prompts.Call("push", promptMessageValue(msg))
	}
	return prompts
}

func promptArgument(prompt, prefill string) js.Value {
	if prefill == "" {
		return js.ValueOf(prompt)
	}
	messages := js.Global().Get("Array").New()
	messages.Call("push", promptMessageValue(promptMessage{
		role:    "user",
		content: prompt,
	}))
	messages.Call("push", promptMessageValue(promptMessage{
		role:    "assistant",
		content: prefill,
		prefix:  true,
	}))
	return messages
}

func promptMessageValue(msg promptMessage) js.Value {
	item := js.Global().Get("Object").New()
	item.Set("role", msg.role)
	item.Set("content", msg.content)
	if msg.prefix {
		item.Set("prefix", true)
	}
	return item
}

func promptCallOptions(opts streamOptions) (js.Value, func(), error) {
	obj := js.Global().Get("Object").New()
	var release func()
	release = func() {}
	if opts.schema != "" {
		schema, err := parseJSON(opts.schema)
		if err != nil {
			return js.Undefined(), release, fmt.Errorf("llm schema: %w", err)
		}
		obj.Set("responseConstraint", schema)
	}
	controller := js.Global().Get("AbortController").New()
	obj.Set("signal", controller.Get("signal"))
	if opts.session != nil {
		opts.session.mu.Lock()
		opts.session.controller = controller
		opts.session.controllerF = release
		opts.session.mu.Unlock()
		release = func() {
			opts.session.mu.Lock()
			opts.session.controller = js.Undefined()
			opts.session.controllerF = nil
			opts.session.mu.Unlock()
		}
	}
	return obj, release, nil
}

func updateSessionContext(s *session, jsSession js.Value) {
	if s == nil {
		return
	}
	contextWindow := jsSession.Get("contextWindow")
	contextUsage := jsSession.Get("contextUsage")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !contextWindow.IsUndefined() && !contextWindow.IsNull() {
		s.contextWin = contextWindow.Int()
	}
	if !contextUsage.IsUndefined() && !contextUsage.IsNull() {
		s.contextUse = contextUsage.Int()
	}
}

func parseJSON(text string) (value js.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			value = js.Undefined()
			err = fmt.Errorf("%v", r)
		}
	}()
	return js.Global().Get("JSON").Call("parse", text), nil
}

func streamChunkBytes(value js.Value) []byte {
	if value.IsUndefined() || value.IsNull() {
		return nil
	}
	if value.Type() == js.TypeString {
		return []byte(value.String())
	}
	uint8Array := js.Global().Get("Uint8Array")
	if !uint8Array.IsUndefined() && value.InstanceOf(uint8Array) {
		data := make([]byte, value.Get("byteLength").Int())
		js.CopyBytesToGo(data, value)
		return data
	}
	arrayBuffer := js.Global().Get("ArrayBuffer")
	if !arrayBuffer.IsUndefined() && value.InstanceOf(arrayBuffer) {
		view := uint8Array.New(value)
		data := make([]byte, view.Get("byteLength").Int())
		js.CopyBytesToGo(data, view)
		return data
	}
	return []byte(value.String())
}

func destroySession(session js.Value) {
	destroy := session.Get("destroy")
	if destroy.IsUndefined() || destroy.IsNull() {
		return
	}
	session.Call("destroy")
}

func languageModelOptions() js.Value {
	return languageModelOptionsBase()
}

func languageModelOptionsWithMonitor() (js.Value, func()) {
	opts := languageModelOptionsBase()
	progress := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		event := args[0]
		setDownloadStatus(DownloadStatus{
			State:  "downloading",
			Loaded: event.Get("loaded").Float(),
		})
		return nil
	})
	monitor := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		args[0].Call("addEventListener", "downloadprogress", progress)
		return nil
	})
	opts.Set("monitor", monitor)
	return opts, func() {
		progress.Release()
		monitor.Release()
	}
}

func languageModelOptionsBase() js.Value {
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
		return js.Undefined(), fmt.Errorf("timeout after %s", timeout)
	}
}
