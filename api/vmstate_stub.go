//go:build !js

package api

import (
	"context"

	"tractor.dev/wanix"
	"tractor.dev/wanix/migration"
)

func collectBundleVMStatesPlatform(root *wanix.Task, ctx context.Context) ([]migration.VMStatePayload, error) {
	return nil, nil
}

func collectBundleTaskStatesPlatform(root *wanix.Task, ctx context.Context) ([]migration.TaskStatePayload, error) {
	return nil, nil
}
