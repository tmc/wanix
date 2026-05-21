//go:build js && wasm

package main

import (
	"embed"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"syscall/js"
	"time"

	_ "embed"

	"tractor.dev/wanix/misc/jsutil"
)

//go:embed lib.js
var assets embed.FS

var v86StatePatchFuncs []js.Func

type vmControl struct {
	id    string
	op    string
	state js.Value
	preVM bool
}

func parseControlMessage(msg js.Value) (vmControl, bool) {
	if !stateProvided(msg) {
		return vmControl{}, false
	}
	op, ok := normalizeControlType(jsStringField(msg, "type"))
	if !ok {
		op, ok = normalizeControlType(jsStringField(msg, "op"))
	}
	if !ok {
		return vmControl{}, false
	}
	state := msg.Get("state")
	if !stateProvided(state) {
		state = msg.Get("initial_state")
	}
	return vmControl{
		id:    jsStringField(msg, "id"),
		op:    op,
		state: state,
	}, true
}

func jsStringField(v js.Value, name string) string {
	if !stateProvided(v) {
		return ""
	}
	field := v.Get(name)
	if field.Type() != js.TypeString {
		return ""
	}
	return field.String()
}

func stateProvided(v js.Value) bool {
	t := v.Type()
	return t != js.TypeUndefined && t != js.TypeNull
}

func stateBuffer(v js.Value) (js.Value, error) {
	if !stateProvided(v) {
		return js.Undefined(), fmt.Errorf("missing state")
	}
	if buffer := v.Get("buffer"); stateProvided(buffer) {
		return buffer, nil
	}
	if stateProvided(v.Get("byteLength")) {
		return v, nil
	}
	return js.Undefined(), fmt.Errorf("state must be ArrayBuffer or typed array")
}

func stateLoadable(v js.Value) (map[string]any, error) {
	buffer, err := stateBuffer(v)
	if err != nil {
		return nil, err
	}
	return map[string]any{"buffer": buffer}, nil
}

func keepJSFunc(fn js.Func) js.Func {
	v86StatePatchFuncs = append(v86StatePatchFuncs, fn)
	return fn
}

func virtio9PDevice(vm js.Value) js.Value {
	v86 := vm.Get("v86")
	if !stateProvided(v86) {
		return js.Undefined()
	}
	cpu := v86.Get("cpu")
	if !stateProvided(cpu) {
		return js.Undefined()
	}
	devices := cpu.Get("devices")
	if !stateProvided(devices) {
		return js.Undefined()
	}
	return devices.Get("virtio_9p")
}

func patchHandle9PState(vm js.Value) {
	dev := virtio9PDevice(vm)
	if !stateProvided(dev) || dev.Get("__wanix_handle9p_state_patch").Truthy() {
		return
	}
	if !stateProvided(dev.Get("handle_fn")) {
		return
	}

	dev.Set("get_state", keepJSFunc(js.FuncOf(func(this js.Value, args []js.Value) any {
		device := this.Get("device")
		return []any{
			device.Get("configspace_tagname"),
			device.Get("configspace_taglen"),
			device.Get("virtio"),
			[]any{},
		}
	})))
	dev.Set("set_state", keepJSFunc(js.FuncOf(func(this js.Value, args []js.Value) any {
		state := args[0]
		device := this.Get("device")
		device.Set("configspace_tagname", state.Index(0))
		device.Set("configspace_taglen", state.Index(1))
		device.Get("virtio").Call("set_state", state.Index(2))
		device.Set("virtqueue", device.Get("virtio").Get("queues").Index(0))
		this.Set("tag_bufchain", js.Global().Get("Map").New(state.Index(3)))
		return nil
	})))
	dev.Set("__wanix_handle9p_state_patch", true)
}

func handle9PInflight(vm js.Value) int {
	dev := virtio9PDevice(vm)
	if !stateProvided(dev) {
		return 0
	}
	tags := dev.Get("tag_bufchain")
	if !stateProvided(tags) {
		return 0
	}
	size := tags.Get("size")
	if size.Type() != js.TypeNumber {
		return 0
	}
	return size.Int()
}

func main() {
	flag.Parse()

	self := js.Global().Get("self")
	controlCh := make(chan vmControl, 32)
	var (
		controlMu       sync.Mutex
		controlStarted  bool
		controlQueue    []vmControl
		initialState    js.Value
		initialStateSet bool
	)
	if globalInitialState := self.Get("initial_state"); stateProvided(globalInitialState) {
		initialState = globalInitialState
		initialStateSet = true
	}
	self.Call("addEventListener", "message", js.FuncOf(func(this js.Value, args []js.Value) any {
		ctl, ok := parseControlMessage(args[0].Get("data"))
		if !ok {
			// todo: handle screen/input/term changes
			return nil
		}
		if controlNeedsState(ctl.op) && !stateProvided(ctl.state) {
			self.Call("postMessage", map[string]any{
				"type":  "v86-control",
				"op":    ctl.op,
				"id":    ctl.id,
				"ok":    false,
				"error": "missing state",
			})
			return nil
		}
		controlMu.Lock()
		if !controlStarted {
			ctl.preVM = true
			controlQueue = append(controlQueue, ctl)
			if ctl.op == controlInitialState {
				initialState = ctl.state
				initialStateSet = true
			}
			controlMu.Unlock()
			return nil
		}
		controlMu.Unlock()
		select {
		case controlCh <- ctl:
		default:
			self.Call("postMessage", map[string]any{
				"type":  "v86-control",
				"op":    ctl.op,
				"id":    ctl.id,
				"ok":    false,
				"error": "control queue full",
			})
		}
		return nil
	}))

	// NOTE: weird call Get on string error when file doesnt exist
	v86wasm, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "#vm/v86/v86.wasm"))
	if err != nil {
		log.Fatal(err)
	}
	v86bios, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "#vm/v86/seabios.bin"))
	if err != nil {
		log.Fatal(err)
	}
	v86vgabios, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "#vm/v86/vgabios.bin"))
	if err != nil {
		log.Fatal(err)
	}
	bzImage, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "boot/bzImage"))
	if err != nil {
		log.Fatal(err)
	}

	// Dynamically import libv86.mjs by creating a Blob URL and using JS dynamic import.
	// Create a Blob from the embedded libv86 bytes
	jslib, err := assets.ReadFile("lib.js")
	if err != nil {
		log.Fatal(err)
	}
	jsbuf := js.Global().Get("Uint8Array").New(len(jslib))
	js.CopyBytesToJS(jsbuf, jslib)
	jslibBlob := js.Global().Get("Blob").New(
		[]any{"var thisWorker = self; var process = undefined;", jsbuf},
		map[string]any{"type": "application/javascript"},
	)
	jslibURL := js.Global().Get("URL").Call("createObjectURL", jslibBlob)

	jsmod, err := jsutil.AwaitErr(js.Global().Call("eval", "(url)=>import(url)").Invoke(jslibURL))
	if err != nil {
		log.Fatalf("failed to import lib.js: %v", err)
	}

	bufObj := func(buf js.Value) map[string]any {
		return map[string]any{
			"buffer": buf.Call("slice").Get("buffer"),
		}
	}
	wasmBlob := js.Global().Get("Blob").New([]any{v86wasm}, map[string]any{"type": "application/wasm"})
	wasmURL := js.Global().Get("URL").Call("createObjectURL", wasmBlob)

	// 9P handler for the v86 emulator. v86's handle9p adapter dispatches
	// requests concurrently and correlates replies by 9P tag (see Wc in
	// lib.js), so we keep a tag-keyed map of per-request reply callbacks.
	// 9P header layout: size[4] type[1] tag[2] -> tag is at offset 5.
	readTag := func(buf js.Value) uint16 {
		return uint16(buf.Index(5).Int()) | uint16(buf.Index(6).Int())<<8
	}
	var (
		p9Mu        sync.Mutex
		p9Callbacks = make(map[uint16]js.Value)
		p9Saving    bool
	)
	p9Pending := func() int {
		p9Mu.Lock()
		defer p9Mu.Unlock()
		return len(p9Callbacks)
	}
	p9SetSaving := func(saving bool) {
		p9Mu.Lock()
		p9Saving = saving
		p9Mu.Unlock()
	}
	js.Global().Get("worker").Get("p9").Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		data := args[0].Get("data")
		tag := readTag(data)
		p9Mu.Lock()
		cb, ok := p9Callbacks[tag]
		if ok {
			delete(p9Callbacks, tag)
		}
		p9Mu.Unlock()
		if ok {
			cb.Invoke(data)
		}
		// Unknown tag: response after Tflush or duplicate -- silently drop.
		return nil
	}))
	p9handler := js.FuncOf(func(this js.Value, args []js.Value) any {
		req := args[0]
		cb := args[1]
		tag := readTag(req)
		p9Mu.Lock()
		if p9Saving {
			p9Mu.Unlock()
			jsutil.Log("p9 request rejected during v86 save-state")
			return nil
		}
		if _, exists := p9Callbacks[tag]; exists {
			jsutil.Log("p9 tag collision on tag", tag)
		}
		p9Callbacks[tag] = cb
		p9Mu.Unlock()
		js.Global().Get("worker").Get("p9").Call("postMessage", req)
		return nil
	})

	cmdline := []string{
		"console=hvc0",
		"init=/bin/init",
		"rw",
		"root=host9p",
		"rootfstype=9p",
		"rootflags=trans=virtio,version=9p2000.L,aname=,cache=none,msize=131072",
		"loglevel=3",
	} // mem=1008M memmap=16M$1008M
	if flag.Arg(0) != "" {
		cmdline = append(cmdline, "export="+flag.Arg(0))
	}
	opts := map[string]any{
		"memory_size":     1024 * 1024 * 1024, // 1GB,
		"vga_memory_size": 8 * 1024 * 1024,    // 8MB
		"cmdline":         strings.Join(cmdline, " "),
		"autostart":       true,
		"wasm_path":       wasmURL,
		"filesystem": map[string]any{
			"handle9p": p9handler,
		},
		"bios":                           bufObj(v86bios),
		"vga_bios":                       bufObj(v86vgabios),
		"bzimage":                        bufObj(bzImage),
		"bzimage_initrd_from_filesystem": false,
		"disable_speaker":                true,
		"disable_mouse":                  true,
		"disable_keyboard":               false,
		"virtio_console":                 true,
		"net_device": map[string]any{
			"type": "virtio",
		},
	}

	initialStateApplied := false
	if initialStateSet {
		loadable, err := stateLoadable(initialState)
		if err != nil {
			log.Fatal(err)
		}
		opts["initial_state"] = loadable
		initialStateApplied = true
	}

	vm := jsmod.Get("V86").New(opts)

	postControl := func(ctl vmControl, ok bool, state js.Value, err error) {
		msg := map[string]any{
			"type": "v86-control",
			"op":   ctl.op,
			"id":   ctl.id,
			"ok":   ok,
		}
		if err != nil {
			msg["error"] = err.Error()
		}
		if stateProvided(state) {
			msg["state"] = state
			self.Call("postMessage", msg, []any{state})
			return
		}
		self.Call("postMessage", msg)
	}
	handleControl := func(ctl vmControl) {
		if ctl.op == controlInitialState && ctl.preVM && initialStateApplied {
			postControl(ctl, true, js.Undefined(), nil)
			return
		}
		switch ctl.op {
		case controlPause:
			_, err := jsutil.AwaitErr(vm.Call("stop"))
			postControl(ctl, err == nil, js.Undefined(), err)
		case controlResume:
			_, err := jsutil.AwaitErr(vm.Call("run"))
			postControl(ctl, err == nil, js.Undefined(), err)
		case controlSaveState:
			if _, err := jsutil.AwaitErr(vm.Call("stop")); err != nil {
				postControl(ctl, false, js.Undefined(), err)
				return
			}
			p9SetSaving(true)
			defer p9SetSaving(false)
			if pending := p9Pending(); pending != 0 {
				postControl(ctl, false, js.Undefined(), fmt.Errorf("%d 9p callbacks in flight", pending))
				return
			}
			patchHandle9PState(vm)
			if pending := handle9PInflight(vm); pending != 0 {
				postControl(ctl, false, js.Undefined(), fmt.Errorf("%d v86 9p requests in flight", pending))
				return
			}
			state, err := jsutil.AwaitErr(vm.Call("save_state"))
			postControl(ctl, err == nil, state, err)
		case controlRestoreState, controlInitialState:
			state, err := stateBuffer(ctl.state)
			if err != nil {
				postControl(ctl, false, js.Undefined(), err)
				return
			}
			if pending := p9Pending(); pending != 0 {
				postControl(ctl, false, js.Undefined(), fmt.Errorf("%d 9p callbacks in flight", pending))
				return
			}
			patchHandle9PState(vm)
			_, err = jsutil.AwaitErr(vm.Call("restore_state", state))
			postControl(ctl, err == nil, js.Undefined(), err)
		default:
			postControl(ctl, false, js.Undefined(), fmt.Errorf("unknown control %q", ctl.op))
		}
	}
	controlMu.Lock()
	queuedControls := append([]vmControl(nil), controlQueue...)
	controlQueue = nil
	controlStarted = true
	controlMu.Unlock()
	go func() {
		for _, ctl := range queuedControls {
			handleControl(ctl)
		}
		for ctl := range controlCh {
			handleControl(ctl)
		}
	}()

	exportch := js.Global().Get("MessageChannel").New()
	// Buffer 9p messages on virtio-console1 output and post complete messages to exportch.port1
	var (
		p9Buf     []byte
		p9MsgLen  int
		p9NeedLen = 4 // First 4 bytes of message is size (LE uint32)
		signaled  bool
	)
	// Run synchronously on the JS callback frame: there are no blocking
	// operations in the body, and spawning a goroutine per byte introduces
	// a race on the shared p9Buf/p9MsgLen accumulator (Go can interleave
	// goroutines at any syscall/js call, and v86 fires this listener once
	// per byte during a burst, so concurrent appends/resets corrupt frames).
	vm.Call("add_listener", "serial0-output-byte", js.FuncOf(func(this js.Value, args []js.Value) any {
		if !signaled {
			signaled = true
			exportch.Get("port1").Call("postMessage", args[0])
			return nil
		}
		b := args[0].Int()
		p9Buf = append(p9Buf, byte(b))

		// First 4 bytes encode the 9P message size (little-endian uint32).
		if len(p9Buf) == p9NeedLen {
			p9MsgLen = int(binary.LittleEndian.Uint32(p9Buf[:4]))
		}

		// Once a full message is accumulated, ship it over and reset.
		if p9MsgLen > 0 && len(p9Buf) == p9MsgLen {
			uint8arr := js.Global().Get("Uint8Array").New(p9MsgLen)
			js.CopyBytesToJS(uint8arr, p9Buf)
			exportch.Get("port1").Call("postMessage", uint8arr)
			p9Buf = p9Buf[:0]
			p9MsgLen = 0
		}
		return nil
	}))
	exportch.Get("port1").Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		// jsutil.Log("in<<", args[0].Get("data"))
		// vm.Get("bus").Call("send", "virtio-console1-input-bytes", args[0].Get("data"))
		// vm.Get("bus").Call("send", "serial1-input", args[0].Get("data"))
		vm.Call("serial_send_bytes", "0", args[0].Get("data"))

		// test := js.Global().Get("Uint8Array").New(8)
		// js.CopyBytesToJS(test, []byte{5, 5, 4, 3, 3, 2, 1, 1})
		// jsutil.Log("in<<", test)
		// vm.Call("serial_send_bytes", "0", test)
		// vm.Call("serial_send_bytes", "1", test)
		return nil
	}))

	vm.Call("add_listener", "virtio-console0-output-bytes", js.FuncOf(func(this js.Value, args []js.Value) any {
		// ugh, these events are still one byte at a time?!?
		go func() {
			jsBuf := args[0]
			buf := make([]byte, jsBuf.Get("byteLength").Int())
			js.CopyBytesToGo(buf, jsBuf)
			fmt.Fprint(os.Stdout, string(buf))
		}()
		return nil
	}))

	sendStdin := func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				jsBuf := js.Global().Get("Uint8Array").New(n)
				js.CopyBytesToJS(jsBuf, buf[:n])
				vm.Get("bus").Call("send", "virtio-console0-input-bytes", jsBuf)
			}
			if err != nil {
				if err != io.EOF {
					log.Println("stdin read error:", err)
				}
				break
			}
		}
	}

	vm.Call("add_listener", "emulator-ready", js.FuncOf(func(this js.Value, args []js.Value) any {
		patchHandle9PState(vm)

		vm.Get("bus").Call("send", "virtio-console0-resize", []any{
			100, 100,
		})

		tmpScreen := js.Global().Get("OffscreenCanvas").New(800, 600)
		screenAdapter := jsmod.Get("OffscreenScreenAdapter").New(tmpScreen, js.FuncOf(func(this js.Value, args []js.Value) any {
			vm.Get("v86").Get("cpu").Get("devices").Get("vga").Call("screen_fill_buffer")
			return nil
		}))
		vm.Get("v86").Get("cpu").Get("devices").Get("vga").Set("screen", screenAdapter)
		vm.Set("screen_adapter", screenAdapter)

		go sendStdin()

		js.Global().Get("self").Call("postMessage", map[string]any{
			"vm":     os.Getenv("vm"),
			"export": exportch.Get("port2"),
		}, []any{exportch.Get("port2")})

		return nil
	}))

	for {
		time.Sleep(time.Hour)
	}
}
