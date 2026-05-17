package main

import "testing"

func TestNormalizeControlType(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"pause", controlPause, true},
		{"stop", controlPause, true},
		{"run", controlResume, true},
		{"checkpoint", controlSaveState, true},
		{"restore_state", controlRestoreState, true},
		{"initial_state", controlInitialState, true},
		{"unknown", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeControlType(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("normalizeControlType(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestControlNeedsState(t *testing.T) {
	for _, op := range []string{controlRestoreState, controlInitialState} {
		if !controlNeedsState(op) {
			t.Fatalf("controlNeedsState(%q) = false", op)
		}
	}
	if controlNeedsState(controlSaveState) {
		t.Fatalf("controlNeedsState(%q) = true", controlSaveState)
	}
}
