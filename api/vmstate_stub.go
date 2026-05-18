//go:build !js

package api

import (
	"context"

	"tractor.dev/wanix"
)

func collectBundleVMStatesPlatform(root *wanix.Task, ctx context.Context) ([]bundleVMState, error) {
	return nil, nil
}
