package llm

import (
	"os"
	"strings"
	"testing"
)

func TestSourceExposesBindableChatFiles(t *testing.T) {
	data, err := os.ReadFile("llm.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, want := range []string{
		`fskit.Entry("chat", fs.ModeDir|0755)`,
		`case "chat/input":`,
		`case "chat/output":`,
		`case "chat/status":`,
		`FS:        "#web/llm"`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("llm.go missing %q", want)
		}
	}
}
