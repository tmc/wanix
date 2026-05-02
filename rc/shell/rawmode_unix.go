//go:build !(js && wasm)

package shell

import "golang.org/x/term"

// makeTerminalRaw puts the terminal into raw mode and returns a restorer.
// On platforms where raw mode is not supported it returns
// errLineEditUnsupported so the caller can fall back to the simple scanner.
func makeTerminalRaw(fd int) (func(), error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, errLineEditUnsupported
	}
	return func() { _ = term.Restore(fd, state) }, nil
}
