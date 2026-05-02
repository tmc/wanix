package web

import (
	"bytes"
	"os"
	"testing"
)

// minimalWasmHeader is a valid wasm magic + version header. Real wasm starts
// with "\x00asm" then a u32 version. Detection only scans for marker
// substrings, so a real header is not strictly required, but we include it so
// fixtures look like wasm to any future stricter check.
var minimalWasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// wasmFixture builds a synthetic wasm-shaped byte slice that contains the
// given fragments verbatim, separated by null bytes. Fragments are written as
// raw bytes (use "\x04gojs" for a length-prefixed module name).
func wasmFixture(fragments ...string) []byte {
	var buf bytes.Buffer
	buf.Write(minimalWasmHeader)
	for _, f := range fragments {
		buf.WriteString(f)
		buf.WriteByte(0)
	}
	return buf.Bytes()
}

func TestWasmHeadHasMarker(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		marker []byte
		want   bool
	}{
		{
			name:   "gojs binary contains length-prefixed gojs module name",
			data:   wasmFixture("\x04gojs\x1cruntime.scheduleTimeoutEvent"),
			marker: gojsMarker,
			want:   true,
		},
		{
			name:   "gojs binary does not contain wasi marker",
			data:   wasmFixture("\x04gojs\x1cruntime.scheduleTimeoutEvent"),
			marker: wasiMarkerPreview1,
			want:   false,
		},
		{
			name:   "wasi preview1 binary contains preview1 marker",
			data:   wasmFixture("wasi_snapshot_preview1"),
			marker: wasiMarkerPreview1,
			want:   true,
		},
		{
			name:   "wasi preview1 binary does not contain gojs marker",
			data:   wasmFixture("wasi_snapshot_preview1"),
			marker: gojsMarker,
			want:   false,
		},
		{
			name:   "wasi unstable binary contains unstable marker",
			data:   wasmFixture("wasi_unstable"),
			marker: wasiMarkerUnstable,
			want:   true,
		},
		{
			name:   "empty data has no markers",
			data:   nil,
			marker: gojsMarker,
			want:   false,
		},
		{
			name:   "header-only has no markers",
			data:   minimalWasmHeader,
			marker: gojsMarker,
			want:   false,
		},
		{
			name:   "marker just inside scan limit is found",
			data:   append(bytes.Repeat([]byte{0}, wasmDetectScanLimit-len(gojsMarker)), gojsMarker...),
			marker: gojsMarker,
			want:   true,
		},
		{
			name:   "marker just past scan limit is not found",
			data:   append(bytes.Repeat([]byte{0}, wasmDetectScanLimit), gojsMarker...),
			marker: gojsMarker,
			want:   false,
		},
		{
			name:   "bare 'gojs' substring without length prefix is not the gojs marker",
			data:   wasmFixture("the word gojs is mentioned in passing"),
			marker: gojsMarker,
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wasmHeadHasMarker(tc.data, tc.marker)
			if got != tc.want {
				t.Errorf("wasmHeadHasMarker(%q...len=%d) = %v, want %v",
					previewBytes(tc.data, 16), len(tc.data), got, tc.want)
			}
		})
	}
}

// TestMarkersAreSpecific guards against regressions where a marker is loosened
// to a substring that could collide with unrelated content. The gojs marker
// must include the wasm length-prefix byte so that it matches only inside an
// actual import-section module-name encoding, not an incidental "gojs"
// substring in wasm string data.
func TestMarkersAreSpecific(t *testing.T) {
	if len(gojsMarker) < 2 || gojsMarker[0] != 0x04 {
		t.Errorf("gojs marker %q must start with 0x04 (the wasm length-prefix for a 4-byte module name)", gojsMarker)
	}
}

// TestRealRcWasmIsGojs is a smoke test against the locally built rc.wasm
// (a real gojs binary). Skipped if dist/rc.wasm is absent so the test suite
// stays runnable on a fresh checkout. When present, this verifies that the
// markers correctly identify a real gojs binary and do not false-positive on
// either wasi marker.
func TestRealRcWasmIsGojs(t *testing.T) {
	data, err := os.ReadFile("../dist/rc.wasm")
	if err != nil {
		t.Skipf("dist/rc.wasm not present, skipping real-binary smoke test: %v", err)
	}
	if !wasmHeadHasMarker(data, gojsMarker) {
		t.Errorf("rc.wasm should match gojs marker")
	}
	if wasmHeadHasMarker(data, wasiMarkerPreview1) {
		t.Errorf("rc.wasm should NOT match wasi_snapshot_preview1 marker")
	}
	if wasmHeadHasMarker(data, wasiMarkerUnstable) {
		t.Errorf("rc.wasm should NOT match wasi_unstable marker")
	}
}

func previewBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
