package wanix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"

	"tractor.dev/toolkit-go/engine/cli"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/vfs"
	"tractor.dev/wanix/migration"
	"tractor.dev/wanix/misc"
)

// contextKey is a value for use with context.WithValue. It's used as
// a pointer so it fits in an interface{} without allocation.
type contextKey struct {
	name string
}

func (k *contextKey) String() string { return "task context value " + k.name }

var (
	TaskContextKey = &contextKey{"task"}
)

func FromContext(ctx context.Context) (*Task, bool) {
	p, ok := ctx.Value(TaskContextKey).(*Task)
	return p, ok
}

type TaskDriver interface {
	Check(*Task) bool
	Start(*Task) error
}

// TaskRestorer is implemented by drivers that can restore a running task from
// its migration manifest after namespace and file descriptors have been rebuilt.
type TaskRestorer interface {
	RestoreTask(*Task, migration.TaskManifest) error
}

type Task struct {
	driver TaskDriver
	parent *Task
	ns     *vfs.NS
	id     int
	alias  string
	kind   string
	cmd    string
	env    []string
	exit   string
	dir    string
	fds    map[int]*openFile
	fdIdx  int
	closer func()
	fsys   *TaskFS
	worker any
	export fs.FS
	mu     sync.Mutex
}

func Export(t *Task, export fs.FS) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.export = export
}

func SetWorker(t *Task, worker any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.worker = worker
}

func GetWorker(t *Task) any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.worker
}

type openFile struct {
	file       fs.File
	path       string
	flags      int
	flagsKnown bool
	offset     int64
	stdio      bool
	mu         sync.Mutex
}

func (f *openFile) Close() error {
	return f.file.Close()
}

func (f *openFile) Stat() (fs.FileInfo, error) {
	return f.file.Stat()
}

func (f *openFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	n, err := f.file.Read(p)
	f.offset += int64(n)
	return n, err
}

func (f *openFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	w, ok := f.file.(io.Writer)
	if !ok {
		return 0, fs.ErrPermission
	}
	n, err := w.Write(p)
	f.offset += int64(n)
	return n, err
}

func (f *openFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	r, ok := f.file.(io.ReaderAt)
	if !ok {
		return 0, fmt.Errorf("%w: ReadAt", fs.ErrNotSupported)
	}
	n, err := r.ReadAt(p, off)
	if s, ok := f.file.(io.Seeker); ok {
		if _, seekErr := s.Seek(f.offset, io.SeekStart); seekErr != nil && err == nil {
			err = seekErr
		}
	}
	return n, err
}

func (f *openFile) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	w, ok := f.file.(io.WriterAt)
	if !ok {
		return 0, fmt.Errorf("%w: WriteAt", fs.ErrNotSupported)
	}
	n, err := w.WriteAt(p, off)
	if s, ok := f.file.(io.Seeker); ok {
		if _, seekErr := s.Seek(f.offset, io.SeekStart); seekErr != nil && err == nil {
			err = seekErr
		}
	}
	return n, err
}

func (f *openFile) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s, ok := f.file.(io.Seeker)
	if !ok {
		return 0, fmt.Errorf("%w: Seek", fs.ErrNotSupported)
	}
	pos, err := s.Seek(offset, whence)
	if err != nil {
		return pos, err
	}
	f.offset = pos
	return pos, nil
}

func (f *openFile) Sync() error {
	return fs.Sync(f.file)
}

func (f *openFile) manifest(fd int) (migration.FDManifest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	m := migration.FDManifest{
		FD:     fd,
		Path:   f.path,
		Flags:  f.flags,
		Offset: f.offset,
		Stdio:  f.stdio,
	}
	if info, err := f.file.Stat(); err == nil {
		m.Kind = fdKind(info.Mode())
	}
	if f.stdio {
		m.Restorable = true
		return m, nil
	}
	var reasons []string
	if f.path == "" {
		reasons = append(reasons, "missing path")
	}
	if !f.flagsKnown {
		reasons = append(reasons, "unknown open flags")
	}
	if _, ok := f.file.(io.Seeker); !ok {
		switch {
		case (m.Kind == "pipe" || m.Kind == "dir") && f.offset == 0:
			// Fresh path-backed special descriptors can be reopened by path.
		case m.Kind == "pipe":
			reasons = append(reasons, "pipe stream offset")
		case m.Kind == "dir":
			reasons = append(reasons, "directory stream offset")
		default:
			reasons = append(reasons, "file is not seekable")
		}
	}
	if len(reasons) != 0 {
		m.Restorable = false
		m.Error = strings.Join(reasons, "; ")
		return m, fmt.Errorf("fd %d %s: %w", fd, m.Error, migration.ErrUnrestorableFD)
	}
	m.Restorable = true
	return m, nil
}

func fdKind(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeNamedPipe != 0:
		return "pipe"
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeDevice != 0 && mode&fs.ModeCharDevice != 0:
		return "char-device"
	case mode&fs.ModeDevice != 0:
		return "device"
	case mode&fs.ModeDir != 0:
		return "dir"
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode&fs.ModeIrregular != 0:
		return "irregular"
	default:
		return "file"
	}
}

// NewRoot returns a task, so we dont really have the TaskFS
// in the public API. For now we have a couple methods that normally
// make more sense on TaskFS, but are on Task.

func (t *Task) Lookup(rid string) (*Task, error) {
	return t.fsys.Lookup(rid)
}

func (t *TaskFS) Lookup(rid string) (*Task, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tt, ok := t.resources[rid]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return tt.(*Task), nil
}

func (t *Task) Tasks() (tasks []*Task) {
	t.fsys.mu.Lock()
	defer t.fsys.mu.Unlock()
	for _, tt := range t.fsys.resources {
		tasks = append(tasks, tt.(*Task))
	}
	return tasks
}

// kludge: this would imply task specific registration, but its global.
// this is until we have a better registration system.
func (t *Task) Register(kind string, driver TaskDriver) {
	t.fsys.types[kind] = driver
}

func (t *Task) Start() error {
	if t.driver != nil {
		return t.driver.Start(t)
	}
	return nil
}

func (r *Task) ID() string {
	return strconv.Itoa(r.id)
}

func (r *Task) Context() context.Context {
	return r.NS().Context()
}

func (r *Task) Export() (fs.FS, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.export == nil {
		return nil, fs.ErrNotExist
	}
	return r.export, nil
}

func (r *Task) NS() *vfs.NS {
	return r.ns
}

func (r *Task) Parent() *Task {
	return r.parent
}

func (r *Task) Root() *Task {
	if r.parent == nil {
		return r
	}
	return r.parent.Root()
}

func (r *Task) Cmd() string {
	return r.cmd
}

func (r *Task) Arg(idx int) string {
	args := strings.Split(r.cmd, " ")
	if idx < 0 || idx >= len(args) {
		return ""
	}
	return args[idx]
}

func (r *Task) Env() []string {
	return r.env
}

func (r *Task) Alias() string {
	return r.alias
}

func (r *Task) Dir() string {
	return r.dir
}

func (r *Task) Bind(srcPath, dstPath string) error {
	return r.ns.Bind(r.ns, srcPath, dstPath)
}

func (r *Task) Unbind(srcPath, dstPath string) error {
	return r.ns.Unbind(r.ns, srcPath, dstPath)
}

func (r *Task) OpenFD(file fs.File, path string) int {
	return r.openFD(file, path, 0, false)
}

func (r *Task) OpenFDWithFlags(file fs.File, path string, flags int) int {
	return r.openFD(file, path, flags, true)
}

func (r *Task) openFD(file fs.File, path string, flags int, flagsKnown bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fdIdx++
	r.fds[r.fdIdx] = newOpenFile(file, path, flags, flagsKnown, false)
	return r.fdIdx
}

func newOpenFile(file fs.File, path string, flags int, flagsKnown, stdio bool) *openFile {
	f := &openFile{
		file:       file,
		path:       path,
		flags:      flags,
		flagsKnown: flagsKnown,
		stdio:      stdio,
	}
	if s, ok := file.(io.Seeker); ok {
		if off, err := s.Seek(0, io.SeekCurrent); err == nil {
			f.offset = off
		}
	}
	return f
}

func (r *Task) CloseFD(fd int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fd < 0 || fd > r.fdIdx {
		return fs.ErrInvalid
	}
	f, ok := r.fds[fd]
	if !ok {
		return fs.ErrInvalid
	}
	delete(r.fds, fd)
	return f.Close()
}

func (r *Task) FD(fd int) (fs.File, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fd < 0 || (fd >= 3 && fd > r.fdIdx) {
		return nil, "", fs.ErrInvalid
	}
	if fd < 3 {
		if _, ok := r.fds[fd]; !ok {
			name := fmt.Sprintf("#task/%s/fd/%d", r.ID(), fd)
			// this should probably use #task/self but i think there are some
			// issues to work out for that to work correctly here.
			stdfile, err := r.NS().Open(name)
			if err != nil {
				return nil, "", err
			}
			r.fds[fd] = newOpenFile(stdfile, name, 0, true, true)
		}
	}
	f, ok := r.fds[fd]
	if !ok {
		return nil, "", fs.ErrInvalid
	}
	return f, f.path, nil
}

func (r *Task) FDManifests() ([]migration.FDManifest, error) {
	r.mu.Lock()
	fds := make([]int, 0, len(r.fds))
	for fd := range r.fds {
		fds = append(fds, fd)
	}
	sort.Ints(fds)
	files := make([]*openFile, len(fds))
	for i, fd := range fds {
		files[i] = r.fds[fd]
	}
	r.mu.Unlock()

	out := make([]migration.FDManifest, 0, len(fds))
	var errs []error
	for i, fd := range fds {
		m, err := files[i].manifest(fd)
		out = append(out, m)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

// Manifest returns the task state needed to restore its namespace and open file
// table from a migration bundle.
func (r *Task) Manifest(resolve vfs.FSIDResolver) (migration.TaskManifest, error) {
	r.mu.Lock()
	id := r.ID()
	state := taskStateLocked(r)
	manifest := migration.TaskManifest{
		ID:        id,
		Kind:      r.kind,
		State:     state,
		Alias:     r.alias,
		Command:   r.cmd,
		Exit:      r.exit,
		Directory: r.dir,
		Env:       append([]string(nil), r.env...),
	}
	ns := r.ns
	r.mu.Unlock()

	var errs []error
	namespace, err := ns.ExportManifest(id, resolve)
	if err != nil {
		errs = append(errs, err)
	}
	manifest.Namespace = namespace
	fds, err := r.FDManifests()
	if err != nil {
		errs = append(errs, err)
	}
	manifest.FDs = fds
	return manifest, errors.Join(errs...)
}

func taskStateLocked(r *Task) migration.TaskState {
	if r.worker != nil {
		return migration.TaskStateRunning
	}
	if r.exit != "" {
		return migration.TaskStateExited
	}
	return migration.TaskStateCreated
}

func checkTaskManifestState(manifest migration.TaskManifest) error {
	switch manifest.State {
	case "", migration.TaskStateCreated, migration.TaskStateExited:
		return nil
	case migration.TaskStateRunning:
		return nil
	default:
		return fmt.Errorf("restore task %s state %q: %w", manifest.ID, manifest.State, migration.ErrInvalidManifest)
	}
}

func checkTaskManifestDriver(manifest migration.TaskManifest, driver TaskDriver) error {
	if manifest.State != migration.TaskStateRunning {
		return nil
	}
	if _, ok := driver.(TaskRestorer); !ok {
		return fmt.Errorf("restore task %s state %q: %w", manifest.ID, manifest.State, migration.ErrUnsupported)
	}
	return nil
}

func restoreTaskRuntime(task *Task, manifest migration.TaskManifest) error {
	if manifest.State != migration.TaskStateRunning {
		return nil
	}
	restorer, ok := task.driver.(TaskRestorer)
	if !ok {
		return fmt.Errorf("restore task %s state %q: %w", manifest.ID, manifest.State, migration.ErrUnsupported)
	}
	if err := restorer.RestoreTask(task, manifest); err != nil {
		return fmt.Errorf("restore task %s runtime: %w", manifest.ID, err)
	}
	return nil
}

func (r *Task) importFDManifests(fds []migration.FDManifest) error {
	opened := make(map[int]*openFile, len(fds))
	fdIdx := r.fdIdx
	fail := func(err error) error {
		closeOpenFiles(opened)
		return err
	}
	for _, manifest := range fds {
		if !manifest.Restorable {
			return fail(fmt.Errorf("restore fd %d %s: %w", manifest.FD, manifest.Error, migration.ErrUnrestorableFD))
		}
		if manifest.FD < 0 {
			return fail(fmt.Errorf("restore fd %d: %w", manifest.FD, migration.ErrUnrestorableFD))
		}
		if manifest.Path == "" {
			return fail(fmt.Errorf("restore fd %d missing path: %w", manifest.FD, migration.ErrUnrestorableFD))
		}
		if err := checkFDManifestKind(manifest); err != nil {
			return fail(fmt.Errorf("restore fd %d %w", manifest.FD, err))
		}
		if _, exists := opened[manifest.FD]; exists {
			return fail(fmt.Errorf("restore fd %d duplicate: %w", manifest.FD, migration.ErrUnrestorableFD))
		}
		file, err := fs.OpenFile(r.NS(), manifest.Path, manifest.Flags, 0)
		if err != nil {
			return fail(fmt.Errorf("restore fd %d %s: %w", manifest.FD, manifest.Path, err))
		}
		if manifest.Offset != 0 {
			if _, err := fs.Seek(file, manifest.Offset, io.SeekStart); err != nil {
				file.Close()
				return fail(fmt.Errorf("restore fd %d seek: %w", manifest.FD, migration.ErrUnrestorableFD))
			}
		}
		opened[manifest.FD] = newOpenFile(file, manifest.Path, manifest.Flags, true, manifest.Stdio)
		if manifest.FD > fdIdx {
			fdIdx = manifest.FD
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fds = opened
	r.fdIdx = fdIdx
	return nil
}

func checkFDManifestKind(manifest migration.FDManifest) error {
	switch manifest.Kind {
	case "", "file":
		return nil
	case "pipe":
		if manifest.Offset != 0 {
			return fmt.Errorf("pipe stream offset: %w", migration.ErrUnrestorableFD)
		}
		return nil
	case "dir":
		if manifest.Offset != 0 {
			return fmt.Errorf("directory stream offset: %w", migration.ErrUnrestorableFD)
		}
		return nil
	default:
		return fmt.Errorf("%s descriptor: %w", manifest.Kind, migration.ErrUnrestorableFD)
	}
}

func closeOpenFiles(files map[int]*openFile) {
	for _, file := range files {
		file.Close()
	}
}

func (r *Task) Open(name string) (fs.File, error) {
	return r.OpenContext(context.Background(), name)
}

func (r *Task) ResolveFS(ctx context.Context, name string) (fs.FS, string, error) {
	m := fskit.MapFS{
		"ctl": misc.ControlFile(&cli.Command{
			Usage: "ctl",
			Short: "control the Task",
			Run: func(ctx *cli.Context, args []string) {
				if len(args) == 3 && args[0] == "bind" {
					if err := r.Bind(args[1], args[2]); err != nil {
						log.Println(err)
					}
					return
				}
				if len(args) == 3 && args[0] == "unbind" {
					if err := r.Unbind(args[1], args[2]); err != nil {
						log.Println(err)
					}
					return
				}
				if len(args) == 1 && args[0] == "start" {
					if err := r.Start(); err != nil {
						log.Println(err)
					}
					return
				}
			},
		}),
		"id":   misc.FieldFile(r.ID()),
		"kind": misc.FieldFile(r.kind),
		"cmd": misc.FieldFile(r.cmd, func(in []byte) error {
			if len(in) > 0 {
				r.cmd = strings.TrimSpace(string(in))
			}
			return nil
		}),
		"alias": misc.FieldFile(r.alias, func(in []byte) error {
			if len(in) > 0 {
				oldalias := r.alias
				r.alias = strings.TrimSpace(string(in))
				r.fsys.mu.Lock()
				if oldalias != "" {
					delete(r.fsys.aliases, oldalias)
				}
				r.fsys.aliases[r.alias] = r
				r.fsys.mu.Unlock()
			}
			return nil
		}),
		"env": misc.FieldFile(strings.Join(r.env, "\n"), func(in []byte) error {
			if len(in) > 0 {
				r.env = strings.Split(strings.TrimSpace(string(in)), "\n")
			}
			return nil
		}),
		"dir": misc.FieldFile(r.dir, func(in []byte) error {
			if len(in) > 0 {
				r.dir = strings.TrimSpace(string(in))
			}
			return nil
		}),
		"exit": misc.FieldFile(r.exit, func(in []byte) error {
			if len(in) > 0 {
				r.exit = strings.TrimSpace(string(in))
				if r.closer != nil {
					go r.closer()
				}
			}
			return nil
		}),
		"binds": fskit.OpenFunc(func(ctx context.Context, name string) (fs.File, error) {
			return fskit.Entry("binds", 0555, []byte(r.NS().String()+"\n")).Open(name)
		}),
		"ns": r.ns,
	}
	if r.export != nil {
		m["export"] = r.export
	}
	return fs.Resolve(m, ctx, name)
}

func (r *Task) OpenContext(ctx context.Context, name string) (fs.File, error) {
	fsys, rname, err := r.ResolveFS(ctx, name)
	if err != nil {
		return nil, err
	}
	return fs.OpenContext(ctx, fsys, rname)
}

type TaskFS struct {
	types     map[string]TaskDriver
	resources map[string]fs.FS
	aliases   map[string]fs.FS
	nextID    int
	mu        sync.Mutex
}

type autoDriver func(*Task) error

func (d autoDriver) Check(*Task) bool {
	return false
}

func (d autoDriver) Start(t *Task) error {
	return d(t)
}

func NewTaskFS() *TaskFS {
	d := &TaskFS{
		types:     make(map[string]TaskDriver),
		resources: make(map[string]fs.FS),
		aliases:   make(map[string]fs.FS),
		nextID:    0,
	}
	// empty namespace process
	d.Register("auto", autoDriver(func(t *Task) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		for kind, driver := range d.types {
			if driver.Check(t) {
				t.kind = kind
				return driver.Start(t)
			}
		}
		return nil
	}))
	return d
}

func (d *TaskFS) Register(kind string, driver TaskDriver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.types[kind] = driver
}

func (d *TaskFS) Alloc(kind string, parent *Task) (*Task, error) {
	d.mu.Lock()
	driver, ok := d.types[kind]
	if !ok {
		d.mu.Unlock()
		return nil, fs.ErrNotExist
	}
	d.nextID++
	id := d.nextID
	rid := strconv.Itoa(id)
	d.mu.Unlock()

	p := &Task{
		fsys:   d,
		driver: driver,
		id:     id,
		kind:   kind,
		fds:    make(map[int]*openFile),
		fdIdx:  3,
	}
	ctx := context.WithValue(context.Background(), TaskContextKey, p)
	if parent != nil {
		p.parent = parent
		p.ns = parent.ns.Clone(ctx)
	} else {
		p.ns = vfs.New(ctx)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resources[rid] = p
	return p, nil
}

// ImportManifest restores a task from a migration manifest. Running tasks are
// restarted only when their driver implements TaskRestorer.
func (d *TaskFS) ImportManifest(ctx context.Context, manifest migration.TaskManifest, parent *Task, lookup vfs.FSIDLookup) (*Task, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := strconv.Atoi(manifest.ID)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("import task %q: %w", manifest.ID, fs.ErrInvalid)
	}
	if manifest.Kind == "" {
		return nil, fmt.Errorf("import task %s: %w", manifest.ID, fs.ErrInvalid)
	}
	if err := checkTaskManifestState(manifest); err != nil {
		return nil, err
	}

	d.mu.Lock()
	driver, ok := d.types[manifest.Kind]
	if !ok {
		d.mu.Unlock()
		return nil, fmt.Errorf("import task %s kind %q: %w", manifest.ID, manifest.Kind, fs.ErrNotExist)
	}
	if _, exists := d.resources[manifest.ID]; exists {
		d.mu.Unlock()
		return nil, fmt.Errorf("import task %s: %w", manifest.ID, fs.ErrExist)
	}
	if manifest.Alias != "" {
		if _, exists := d.aliases[manifest.Alias]; exists {
			d.mu.Unlock()
			return nil, fmt.Errorf("import task alias %q: %w", manifest.Alias, fs.ErrExist)
		}
	}
	if err := checkTaskManifestDriver(manifest, driver); err != nil {
		d.mu.Unlock()
		return nil, err
	}
	startNextID := d.nextID
	d.mu.Unlock()

	task := &Task{
		fsys:   d,
		driver: driver,
		parent: parent,
		id:     id,
		alias:  manifest.Alias,
		kind:   manifest.Kind,
		cmd:    manifest.Command,
		exit:   manifest.Exit,
		env:    append([]string(nil), manifest.Env...),
		dir:    manifest.Directory,
		fds:    make(map[int]*openFile),
		fdIdx:  3,
	}
	taskCtx := context.WithValue(ctx, TaskContextKey, task)
	namespace, err := vfs.ImportManifest(taskCtx, manifest.Namespace, lookup)
	if err != nil {
		return nil, err
	}
	task.ns = namespace
	if err := task.importFDManifests(manifest.FDs); err != nil {
		return nil, err
	}

	d.mu.Lock()
	if _, exists := d.resources[manifest.ID]; exists {
		d.mu.Unlock()
		closeOpenFiles(task.fds)
		return nil, fmt.Errorf("import task %s: %w", manifest.ID, fs.ErrExist)
	}
	if manifest.Alias != "" {
		if _, exists := d.aliases[manifest.Alias]; exists {
			d.mu.Unlock()
			closeOpenFiles(task.fds)
			return nil, fmt.Errorf("import task alias %q: %w", manifest.Alias, fs.ErrExist)
		}
		d.aliases[manifest.Alias] = task
	}
	d.resources[manifest.ID] = task
	if d.nextID < id {
		d.nextID = id
	}
	d.mu.Unlock()
	if err := restoreTaskRuntime(task, manifest); err != nil {
		d.rollbackImportedTasks([]*Task{task}, startNextID)
		return nil, err
	}
	return task, nil
}

func (d *TaskFS) restoreExistingTask(ctx context.Context, task *Task, manifest migration.TaskManifest, lookup vfs.FSIDLookup) (taskRestore, error) {
	id, err := strconv.Atoi(manifest.ID)
	if err != nil || id <= 0 || manifest.ID != task.ID() {
		return taskRestore{}, fmt.Errorf("restore task %q: %w", manifest.ID, fs.ErrInvalid)
	}
	if manifest.Kind == "" {
		return taskRestore{}, fmt.Errorf("restore task %s: %w", manifest.ID, fs.ErrInvalid)
	}
	if err := checkTaskManifestState(manifest); err != nil {
		return taskRestore{}, err
	}

	d.mu.Lock()
	driver, ok := d.types[manifest.Kind]
	if !ok {
		d.mu.Unlock()
		return taskRestore{}, fmt.Errorf("restore task %s kind %q: %w", manifest.ID, manifest.Kind, fs.ErrNotExist)
	}
	if manifest.Alias != "" {
		if existing, exists := d.aliases[manifest.Alias]; exists && existing != task {
			d.mu.Unlock()
			return taskRestore{}, fmt.Errorf("restore task alias %q: %w", manifest.Alias, fs.ErrExist)
		}
	}
	if err := checkTaskManifestDriver(manifest, driver); err != nil {
		d.mu.Unlock()
		return taskRestore{}, err
	}
	d.mu.Unlock()

	task.mu.Lock()
	oldDriver := task.driver
	oldAlias := task.alias
	oldKind := task.kind
	oldCmd := task.cmd
	oldExit := task.exit
	oldEnv := append([]string(nil), task.env...)
	oldDir := task.dir
	oldNS := task.ns
	oldFDs := task.fds
	oldFDIdx := task.fdIdx
	oldWorker := task.worker
	task.mu.Unlock()

	taskCtx := context.WithValue(ctx, TaskContextKey, task)
	namespace, err := vfs.ImportManifest(taskCtx, manifest.Namespace, lookup)
	if err != nil {
		return taskRestore{}, err
	}
	tmp := &Task{
		ns:    namespace,
		fds:   make(map[int]*openFile),
		fdIdx: 3,
	}
	if err := tmp.importFDManifests(manifest.FDs); err != nil {
		return taskRestore{}, err
	}
	newFDs := tmp.fds
	newFDIdx := tmp.fdIdx

	apply := func(driver TaskDriver, alias, kind, cmd, exit string, env []string, dir string, ns *vfs.NS, fds map[int]*openFile, fdIdx int, worker any) {
		task.mu.Lock()
		task.driver = driver
		task.alias = alias
		task.kind = kind
		task.cmd = cmd
		task.exit = exit
		task.env = append([]string(nil), env...)
		task.dir = dir
		task.ns = ns
		task.fds = fds
		task.fdIdx = fdIdx
		task.worker = worker
		task.mu.Unlock()
	}
	setAlias := func(old, new string) {
		d.mu.Lock()
		if old != "" {
			if existing, ok := d.aliases[old]; ok && existing == task {
				delete(d.aliases, old)
			}
		}
		if new != "" {
			d.aliases[new] = task
		}
		d.mu.Unlock()
	}

	apply(driver, manifest.Alias, manifest.Kind, manifest.Command, manifest.Exit, manifest.Env, manifest.Directory, namespace, newFDs, newFDIdx, nil)
	setAlias(oldAlias, manifest.Alias)

	restore := taskRestore{
		rollback: func() {
			closeOpenFiles(newFDs)
			apply(oldDriver, oldAlias, oldKind, oldCmd, oldExit, oldEnv, oldDir, oldNS, oldFDs, oldFDIdx, oldWorker)
			setAlias(manifest.Alias, oldAlias)
		},
		commit: func() {
			closeOpenFiles(oldFDs)
		},
	}
	if err := restoreTaskRuntime(task, manifest); err != nil {
		restore.rollback()
		return taskRestore{}, err
	}
	return restore, nil
}

func (d *TaskFS) ResolveFS(ctx context.Context, name string) (fs.FS, string, error) {
	m := fskit.MapFS{
		"new": fskit.OpenFunc(func(ctx context.Context, name string) (fs.File, error) {
			if name == "." {
				var nodes []fs.DirEntry
				for kind := range d.types {
					nodes = append(nodes, fskit.Entry(kind, 0555))
				}
				return fskit.DirFile(fskit.Entry("new", 0555), nodes...), nil
			}
			return &fskit.FuncFile{
				Node: fskit.Entry(name, 0555),
				ReadFunc: func(n *fskit.Node) (err error) {
					t, found := FromContext(ctx)
					if !found {
						t, err = d.Lookup("1")
						if err != nil {
							return err
						}
					}
					p, err := d.Alloc(name, t)
					if err != nil {
						return err
					}
					fskit.SetData(n, []byte(p.ID()+"\n"))
					return nil
				},
			}, nil
		}),
	}
	fsys := vfs.New(ctx)
	if err := fsys.Bind(fskit.MapFS(d.aliases), ".", "."); err != nil {
		return nil, "", err
	}
	if err := fsys.Bind(fskit.MapFS(d.resources), ".", "."); err != nil {
		return nil, "", err
	}
	if err := fsys.Bind(m, ".", "."); err != nil {
		return nil, "", err
	}
	t, ok := FromContext(ctx)
	if ok {
		if _, exists := d.resources[t.ID()]; exists {
			if err := fsys.Bind(d.resources[t.ID()], ".", "self"); err != nil {
				return nil, "", err
			}
		}
	}
	return fs.Resolve(fsys, ctx, name)
}

func (d *TaskFS) Stat(name string) (fs.FileInfo, error) {
	log.Println("bare stat:", name)
	return d.StatContext(context.Background(), name)
}

func (d *TaskFS) StatContext(ctx context.Context, name string) (fs.FileInfo, error) {
	fsys, rname, err := d.ResolveFS(ctx, name)
	if err != nil {
		return nil, err
	}
	return fs.StatContext(ctx, fsys, rname)
}

func (d *TaskFS) Open(name string) (fs.File, error) {
	log.Println("bare open:", name)
	return d.OpenContext(context.Background(), name)
}

func (d *TaskFS) OpenContext(ctx context.Context, name string) (fs.File, error) {
	fsys, rname, err := d.ResolveFS(ctx, name)
	if err != nil {
		return nil, err
	}
	return fs.OpenContext(ctx, fsys, rname)
}
