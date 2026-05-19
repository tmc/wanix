package gojs

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
			re:   `(?s)func\s+\(d\s+\*Driver\)\s+start\s*\(\s*t\s+\*wanix\.Task\s*,\s*state\s+\[\]byte\s*\).*?worker\.StartTaskWorkerWithState\(\s*d\.Workers\s*,\s*t\s*,\s*gojsworker\.BlobURL\(\)\s*,\s*state\s*\)`,
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
			name: "checkpoint helper import",
			re:   `(?s)import\s+\{.*?isWanixCheckpointSaveState.*?postWanixCheckpointSaveState.*?wanixCheckpointStateFromWorker.*?\}\s+from\s+"\.\/lib\.js"`,
		},
		{
			name: "checkpoint message handler",
			re:   `(?s)isWanixCheckpointSaveState\(\s*message\s*\).*?postWanixCheckpointSaveState\(\s*message\s*\)`,
		},
		{
			name: "checkpoint state exposure",
			re:   `(?s)wanixCheckpointStateFromWorker\(\s*message\.worker\s*\).*?globalThis\.checkpoint_state\s*=\s*checkpointState`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("worker.js missing %s", tt.name)
		}
	}

	helper, err := os.ReadFile("../api/checkpoint.js")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)typeof\s+save\s*!==\s*"function".*?ok:\s*false.*?error:\s*"migration unsupported"`).Match(helper) {
		t.Fatalf("checkpoint helper missing unsupported fail-closed response")
	}
}
