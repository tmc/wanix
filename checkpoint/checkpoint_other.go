//go:build !js

package checkpoint

// Register reports that checkpoints are only available in JavaScript workers.
func Register(h Handler) error {
	return ErrUnsupported
}
