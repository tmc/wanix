//go:build js && wasm

package web

import (
	"log"
	"syscall/js"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/pipe"
	"tractor.dev/wanix/gojs"
	"tractor.dev/wanix/misc/jsutil"
	"tractor.dev/wanix/wasi"
	"tractor.dev/wanix/web/caches"
	"tractor.dev/wanix/web/dl"
	"tractor.dev/wanix/web/dom"
	"tractor.dev/wanix/web/fsa"
	"tractor.dev/wanix/web/sw"
	"tractor.dev/wanix/web/sys"
	"tractor.dev/wanix/web/worker"
)

func New(root *wanix.Task) fskit.MapFS {
	workerfs := worker.New(root)
	webfs := fskit.MapFS{
		"console": jsutil.ConsoleFS,
		"dom":     dom.New(),
		"caches":  caches.New(),
		"worker":  workerfs,
		"dl":      dl.New(),
	}
	opfs, err := fsa.OPFS()
	if err != nil {
		log.Println("opfs:", err)
	} else {
		webfs["opfs"] = opfs
	}
	if script, scope, ok := serviceWorkerConfig(); ok {
		service, err := sw.Activate(script, scope, root)
		if err != nil {
			log.Println("service worker:", err)
		} else {
			webfs["sw"] = service
		}
	}

	root.Register("js", &JSDriver{Workers: workerfs, Root: root})
	root.Register("wasi", &wasi.Driver{Workers: workerfs})
	root.Register("gojs", &gojs.Driver{Workers: workerfs})
	return webfs
}

func serviceWorkerConfig() (script, scope string, ok bool) {
	el := sys.Element()
	if !el.Call("hasAttribute", "service-worker").Bool() {
		return "", "", false
	}
	script = "./wanix-sw.js"
	if attr := el.Call("getAttribute", "service-worker"); attr.Type() == js.TypeString && attr.String() != "" {
		script = attr.String()
	}
	if attr := el.Call("getAttribute", "service-worker-scope"); attr.Type() == js.TypeString {
		scope = attr.String()
	}
	return script, scope, true
}

type dataFile struct {
	js.Value
	buf *pipe.Buffer
}

func (s *dataFile) Stat() (fs.FileInfo, error) {
	return fskit.Entry("data", 0644), nil
}

func (s *dataFile) Write(p []byte) (n int, err error) {
	buf := js.Global().Get("Uint8Array").New(len(p))
	n = js.CopyBytesToJS(buf, p)
	s.Value.Call("send", buf)
	return
}

func (s *dataFile) Read(p []byte) (int, error) {
	return s.buf.Read(p)
}

func (s *dataFile) Close() error {
	return nil
}
