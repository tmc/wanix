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
		`fskit.Entry("clone", 0444)`,
		`fskit.Entry("ctl", 0222)`,
		`fskit.Entry("model", fs.ModeDir|0755)`,
		`fskit.Entry("new", 0444)`,
		`fskit.Entry("params", 0444)`,
		`fskit.Entry("system", 0666)`,
		`case "ctl":`,
		`case "clone":`,
		`case "new":`,
		`func (fsys *FS) WriteFile(name string, data []byte, perm fs.FileMode) error`,
		`func (fsys *FS) OpenFile(name string, flag int, perm fs.FileMode)`,
		`func newPromptInputFile(name string) *promptInputFile`,
		`func availabilityFile(name string) fs.File`,
		`func paramsFile(name string) fs.File`,
		`func newGlobalSystemFile(name string) *globalSystemFile`,
		`func globalSystemPrompt() string`,
		`func setGlobalSystemPrompt(text string)`,
		`func openSessionPath(name string)`,
		`func (s *session) start(prompt string) error`,
		`func (s *session) browserSession() (js.Value, error)`,
		`func runPromptWithSession(session js.Value, prompt string, out chan<- promptOutcome, opts streamOptions) error`,
		`func readSessionOutput(s *session, pending *[]byte, off *int, b []byte) (int, error)`,
		`type sessionContextFile struct`,
		`func sessionContextMetricFile(s *session, name string) fs.File`,
		`func cloneSession(src *session) *session`,
		`func combinedSystemPrompt(local string) string`,
		`func initialPrompts(system string, history []promptMessage) js.Value`,
		`func promptArgument(prompt, prefill string) js.Value`,
		`func promptCallOptions(opts streamOptions) (js.Value, func(), error)`,
		`responseConstraint`,
		`prefix`,
		`contextWindow`,
		`contextUsage`,
		`case "history":`,
		`case "clone":`,
		`case "context":`,
		`case "ctx":`,
		`case "ctx/window", "ctx/usage", "ctx/left":`,
		`case "continue":`,
		`case "stop":`,
		`case "prefill":`,
		`case "schema":`,
		`case "system":`,
		`case "model/availability":`,
		`case "model/ctl":`,
		`case "model/params":`,
		`case "model/status":`,
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
