package wanix

import (
	"os"
	"regexp"
	"testing"
)

func TestSystemElementConvenienceAPIs(t *testing.T) {
	data, err := os.ReadFile("elements/system.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, tt := range []struct {
		name string
		re   string
	}{
		{
			name: "ready",
			re:   `(?s)async\s+ready\s*\(\s*\).*?this\.isReady.*?this\._ready`,
		},
		{
			name: "start task",
			re:   `(?s)async\s+startTask\s*\(\s*options\s*=\s*\{\}\s*\).*?#task.*?new.*?options\.cmd.*?options\.alias.*?options\.dir.*?options\.env.*?task\.bind\(\s*path\s*,\s*"#task/self"\s*\).*?ctl.*?start`,
		},
		{
			name: "terminal task",
			re:   `(?s)async\s+bindTaskTerminal\s*\(\s*task\s*\).*?#term/new.*?fd.*?0.*?1.*?2.*?async\s+startTerminalTask\s*\(\s*options\s*=\s*\{\}\s*\).*?this\.startTask\(\s*\{\s*\.\.\.options\s*,\s*term:\s*true\s*\}`,
		},
		{
			name: "terminal attachment",
			re:   `(?s)async\s+attachTerminal\s*\(\s*host\s*,\s*path\s*,\s*options\s*=\s*\{\}\s*\).*?document\.createElement\(\s*"wanix-term"\s*\).*?host\.replaceChildren\(\s*term\s*\).*?term\.connect\(\s*\)`,
		},
		{
			name: "terminal write",
			re:   `(?s)async\s+writeTerminal\s*\(\s*path\s*,\s*text\s*\).*?openWritable\(\s*\[\s*path\s*,\s*"data"\s*\]\.join\("/"\)\s*\).*?TextEncoder\(\)\.encode\(\s*text\s*\)`,
		},
	} {
		if !regexp.MustCompile(tt.re).MatchString(src) {
			t.Fatalf("elements/system.js missing %s helper", tt.name)
		}
	}
}
