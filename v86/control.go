package main

import "strings"

const (
	controlPause        = "pause"
	controlResume       = "resume"
	controlSaveState    = "save-state"
	controlRestoreState = "restore-state"
	controlInitialState = "initial-state"
)

func normalizeControlType(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pause", "stop":
		return controlPause, true
	case "resume", "run", "start":
		return controlResume, true
	case "save-state", "savestate", "checkpoint":
		return controlSaveState, true
	case "restore-state", "restore_state", "restorestate", "restore":
		return controlRestoreState, true
	case "initial-state", "initial_state":
		return controlInitialState, true
	default:
		return "", false
	}
}

func controlNeedsState(op string) bool {
	return op == controlRestoreState || op == controlInitialState
}
