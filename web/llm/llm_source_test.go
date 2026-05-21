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
		`fskit.Entry("ctl", 0222)`,
		`fskit.Entry("new", 0444)`,
		`case "ctl":`,
		`case "new":`,
		`func (fsys *FS) OpenFile(name string, flag int, perm fs.FileMode)`,
		`func openSessionPath(name string)`,
		`func (s *session) start(prompt string) error`,
		`case "chat/input":`,
		`case "chat/output":`,
		`case "chat/status":`,
		`case "download", "start":`,
		`func (f *ctlFile) Truncate(int64) error`,
		`Download     DownloadStatus`,
		`downloadprogress`,
		`promptStreaming`,
		`streamPrompt(prompt, f.results)`,
		`FS:        "#web/llm"`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("llm.go missing %q", want)
		}
	}
}
