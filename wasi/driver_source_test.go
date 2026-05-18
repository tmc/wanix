package wasi

import (
	"os"
	"regexp"
	"testing"
)

func TestDriverRestoresCheckpointState(t *testing.T) {
	data, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "restore reads manifest state path",
			re:   `(?s)func\s+\(d\s+\*Driver\)\s+RestoreTask\s*\(\s*t\s+\*wanix\.Task\s*,\s*manifest\s+migration\.TaskManifest\s*\).*?manifest\.StatePath\s*!=\s*""\s*.*?fs\.ReadFile\(\s*t\.NS\(\)\s*,\s*manifest\.StatePath\s*\)`,
		},
		{
			name: "restore starts worker with state",
			re:   `(?s)func\s+\(d\s+\*Driver\)\s+start\s*\(\s*t\s+\*wanix\.Task\s*,\s*state\s+\[\]byte\s*\).*?worker\.StartTaskWorkerWithState\(\s*d\.Workers\s*,\s*t\s*,\s*wasiworker\.BlobURL\(\)\s*,\s*state\s*\)`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("driver.go missing %s", tt.name)
		}
	}
}

func TestWorkerSupportsCheckpointMessages(t *testing.T) {
	data, err := os.ReadFile("worker/worker.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "checkpoint message handler",
			re:   `(?s)message\.type\s*===\s*"wanix-checkpoint".*?message\.op\s*===\s*"save-state".*?saveCheckpoint\(\s*message\s*\)`,
		},
		{
			name: "checkpoint state exposure",
			re:   `(?s)message\.worker\.checkpoint_state.*?globalThis\.checkpoint_state\s*=\s*message\.worker\.checkpoint_state`,
		},
		{
			name: "child worker checkpoint state",
			re:   `(?s)worker\.postMessage\(\s*\{.*?checkpoint_state:\s*message\.worker\.checkpoint_state`,
		},
		{
			name: "unsupported fail closed",
			re:   `(?s)typeof\s+globalThis\.wanixCheckpointState\s*!==\s*"function".*?ok:\s*false.*?error:\s*"migration unsupported"`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("worker.js missing %s", tt.name)
		}
	}
}
