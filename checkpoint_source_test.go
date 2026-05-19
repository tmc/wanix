package wanix

import (
	"os"
	"regexp"
	"testing"
)

func TestWebJSDriverInstallsCheckpointRuntime(t *testing.T) {
	data, err := os.ReadFile("web/jsdriver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "runtime prefix",
			re:   `append\(\[\]byte\(jsCheckpointRuntime\),\s*data\.\.\.\)`,
		},
		{
			name: "protocol version",
			re:   `version:\s*1`,
		},
		{
			name: "global helper",
			re:   `globalThis\.WanixCheckpoint\s*=`,
		},
		{
			name: "install hook",
			re:   `(?s)function\s+install\(options\s*=\s*\{\}\).*?postSaveState`,
		},
		{
			name: "fail closed without save hook",
			re:   `(?s)typeof\s+save\s*!==\s*"function".*?migration unsupported`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("web/jsdriver.go missing %s", tt.name)
		}
	}
}
