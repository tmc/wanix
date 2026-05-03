package web

import (
	"bytes"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
)

const wasmDetectScanLimit = 16384

// wasmMagic is the 8-byte preamble of every wasm module: "\0asm" + version 1.
// We check this before scanning for runtime markers so a bare PATH name like
// "warren" (no .wasm suffix) is still classified by content, not extension.
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// Markers used to identify wasm task runtime expectations by inspecting the
// raw bytes of the binary's import section. The wasm import format encodes
// each import as a length-prefixed module name followed by a length-prefixed
// field name; the module and field strings are not joined by a literal '.',
// so a marker like "gojs.runtime." would never match. The gojs marker is the
// wire encoding of the 4-byte module name "gojs" (length-prefix 0x04 + the
// four ASCII bytes), which is distinctive enough to avoid colliding with
// incidental occurrences of "gojs" in unrelated wasm string data.
var (
	wasiMarkerPreview1 = []byte("wasi_snapshot_preview1")
	wasiMarkerUnstable = []byte("wasi_unstable")
	gojsMarker         = []byte("\x04gojs")
)

func wasmHeadHasMarker(data, marker []byte) bool {
	if !bytes.HasPrefix(data, wasmMagic) {
		return false
	}
	if len(data) > wasmDetectScanLimit {
		data = data[:wasmDetectScanLimit]
	}
	return bytes.Contains(data, marker)
}

func taskWasmHasMarker(t *wanix.Task, marker []byte) bool {
	data, err := fs.ReadFile(t.Namespace(), t.Arg(0))
	if err != nil {
		return false
	}
	return wasmHeadHasMarker(data, marker)
}
