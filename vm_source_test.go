package wanix

import (
	"os"
	"regexp"
	"testing"
)

func TestVMElementPassesExportArgument(t *testing.T) {
	data, err := os.ReadFile("elements/vm.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "reads export attribute",
			re:   `this\.export\s*=\s*this\.getAttribute\(['"]export['"]\)\s*\|\|\s*[""]`,
		},
		{
			name: "passes export argument",
			re:   `this\.task\.cmd\s*=\s*` + "`" + `#vm/\$\{this\.type\}/\$\{this\.type\}-vm\.wasm\s+\$\{this\.export\}` + "`",
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("elements/vm.js missing %s", tt.name)
		}
	}
}
