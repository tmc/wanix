package shell

import (
	"encoding/json"
	"errors"
	"testing"

	"tractor.dev/wanix/checkpoint"
)

func TestCheckpointStateSavesPromptBoundary(t *testing.T) {
	state := newCheckpointState("/home", []string{"PATH=/bin"})
	state.addHistory("cd /tmp")
	state.addHistory("export USER=glenda")
	state.recordCall([]string{"export", "USER=glenda"})
	state.recordCall([]string{"unset", "PATH"})
	state.setStatus("/tmp", 7)

	data, err := state.save()
	if err != nil {
		t.Fatal(err)
	}

	var saved rcCheckpoint
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.Dir != "/tmp" {
		t.Fatalf("saved checkpoint = %#v", saved)
	}
	if got, want := saved.Env, []string{"USER=glenda"}; !equalStrings(got, want) {
		t.Fatalf("env = %#v, want %#v", got, want)
	}
	if got, want := saved.History, []string{"cd /tmp", "export USER=glenda"}; !equalStrings(got, want) {
		t.Fatalf("history = %#v, want %#v", got, want)
	}
}

func TestCheckpointStateFailsWhileRunning(t *testing.T) {
	state := newCheckpointState("/", nil)
	state.setRunning("/")

	_, err := state.save()
	if !errors.Is(err, checkpoint.ErrUnsupported) {
		t.Fatalf("save error = %v, want ErrUnsupported", err)
	}
}

func TestCheckpointStateLoads(t *testing.T) {
	data, err := json.Marshal(rcCheckpoint{
		Version: 1,
		Dir:     "/mnt",
		Env:     []string{"A=B"},
		History: []string{"pwd", "history"},
	})
	if err != nil {
		t.Fatal(err)
	}

	state := newCheckpointState("/", nil)
	if err := state.load(data); err != nil {
		t.Fatal(err)
	}
	if got := state.dir(); got != "/mnt" {
		t.Fatalf("dir = %q, want /mnt", got)
	}
	if got, want := state.env(), []string{"A=B"}; !equalStrings(got, want) {
		t.Fatalf("env = %#v, want %#v", got, want)
	}
	if got, want := state.history(), []string{"pwd", "history"}; !equalStrings(got, want) {
		t.Fatalf("history = %#v, want %#v", got, want)
	}
}

func TestCheckpointStateTrimsHistory(t *testing.T) {
	state := newCheckpointState("/", nil)
	for i := 0; i < maxCheckpointHistory+5; i++ {
		state.addHistory("pwd")
	}
	if got := len(state.history()); got != maxCheckpointHistory {
		t.Fatalf("history length = %d, want %d", got, maxCheckpointHistory)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
