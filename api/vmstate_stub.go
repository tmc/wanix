//go:build !js

package api

import (
	"context"

	"tractor.dev/wanix"
)

func collectBundleVMStatesPlatform(root *wanix.Task, ctx context.Context) ([]bundleVMState, error) {
	return nil, nil
}

func collectBundleTaskStatesPlatform(root *wanix.Task, ctx context.Context) ([]bundleTaskState, error) {
	return nil, nil
}
