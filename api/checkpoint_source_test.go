package api

import (
	"os"
	"regexp"
	"testing"
)

func TestCheckpointRuntimeContractSource(t *testing.T) {
	data, err := os.ReadFile("checkpoint.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "protocol version",
			re:   `version:\s*1`,
		},
		{
			name: "protocol type",
			re:   `type:\s*"wanix-checkpoint"`,
		},
		{
			name: "save-state operation",
			re:   `saveStateOp:\s*"save-state"`,
		},
		{
			name: "version compatibility",
			re:   `message\.version\s*===\s*undefined\s*\|\|\s*message\.version\s*===\s*WanixCheckpointProtocol\.version`,
		},
		{
			name: "fail closed without save hook",
			re:   `(?s)typeof\s+save\s*!==\s*"function".*?ok:\s*false.*?error:\s*"migration unsupported"`,
		},
		{
			name: "global helper export",
			re:   `globalThis\.WanixCheckpoint\s*=\s*WanixCheckpoint`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("checkpoint.js missing %s", tt.name)
		}
	}
}
