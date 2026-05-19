// Package checkpoint lets Go WebAssembly programs cooperate with Wanix bundle
// migration.
package checkpoint

import "errors"

// ErrUnsupported reports that the program cannot provide a checkpoint now.
var ErrUnsupported = errors.New("checkpoint unsupported")

// Handler saves and loads program checkpoint state.
type Handler struct {
	// Load restores state provided by Wanix when the task starts.
	Load func([]byte) error

	// Save returns state for Wanix to store in a migration bundle.
	Save func() ([]byte, error)
}
