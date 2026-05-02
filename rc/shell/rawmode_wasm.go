//go:build js && wasm

package shell

// makeTerminalRaw is a no-op on js/wasm. wanix's terminal already delivers
// raw byte streams (no kernel line discipline), so term.NewTerminal can drive
// it directly without flipping any termios bits.
func makeTerminalRaw(fd int) (func(), error) {
	return nil, nil
}
