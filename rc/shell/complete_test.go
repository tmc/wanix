package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWordStart(t *testing.T) {
	tests := []struct {
		line string
		pos  int
		want int
	}{
		{"foo", 3, 0},
		{"echo foo", 8, 5},
		{"echo  foo", 9, 6},
		{"a b c", 5, 4},
		{"", 0, 0},
		{"  leading", 9, 2},
	}
	for _, tc := range tests {
		got := wordStart(tc.line, tc.pos)
		if got != tc.want {
			t.Errorf("wordStart(%q, %d) = %d, want %d", tc.line, tc.pos, got, tc.want)
		}
	}
}

func TestSplitWord(t *testing.T) {
	tests := []struct {
		word, dir, prefix string
	}{
		{"", ".", ""},
		{"foo", ".", "foo"},
		{"foo/bar", "foo/", "bar"},
		{"foo/", "foo/", ""},
		{"/abs/path", "/abs/", "path"},
		{"/", "/", ""},
	}
	for _, tc := range tests {
		dir, prefix := splitWord(tc.word)
		if dir != tc.dir || prefix != tc.prefix {
			t.Errorf("splitWord(%q) = (%q, %q), want (%q, %q)", tc.word, dir, prefix, tc.dir, tc.prefix)
		}
	}
}

func TestCommonPrefix(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"foo"}, "foo"},
		{[]string{"foo", "foobar"}, "foo"},
		{[]string{"foo", "fox"}, "fo"},
		{[]string{"foo", "bar"}, ""},
		{[]string{"abc", "abd", "abe"}, "ab"},
	}
	for _, tc := range tests {
		got := commonPrefix(tc.in)
		if got != tc.want {
			t.Errorf("commonPrefix(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPathCompleteUnique(t *testing.T) {
	dir := t.TempDir()
	mustCreate(t, filepath.Join(dir, "alpha"))
	mustCreate(t, filepath.Join(dir, "beta"))

	line := dir + "/al"
	pos := len(line)
	newLine, newPos, ok := pathComplete(line, pos, completeKey)
	if !ok {
		t.Fatal("expected completion, got ok=false")
	}
	wantLine := dir + "/alpha"
	if newLine != wantLine {
		t.Errorf("line = %q, want %q", newLine, wantLine)
	}
	if newPos != len(wantLine) {
		t.Errorf("pos = %d, want %d", newPos, len(wantLine))
	}
}

func TestPathCompleteCommonPrefixExtends(t *testing.T) {
	dir := t.TempDir()
	mustCreate(t, filepath.Join(dir, "alpha"))
	mustCreate(t, filepath.Join(dir, "alpine"))
	mustCreate(t, filepath.Join(dir, "zeta"))

	line := dir + "/al"
	pos := len(line)
	newLine, _, ok := pathComplete(line, pos, completeKey)
	if !ok {
		t.Fatal("expected completion, got ok=false")
	}
	// alpha and alpine share "alp" — should extend "al" to "alp"
	want := dir + "/alp"
	if newLine != want {
		t.Errorf("line = %q, want %q", newLine, want)
	}
}

func TestPathCompleteCommonPrefixNoExtension(t *testing.T) {
	dir := t.TempDir()
	mustCreate(t, filepath.Join(dir, "alpha"))
	mustCreate(t, filepath.Join(dir, "beta"))

	// "a" matches only alpha, so this is actually unique completion. Use a
	// case where the prefix equals the longest common prefix of multiple
	// matches: "al" already shared, no further chars to extend.
	mustCreate(t, filepath.Join(dir, "alpine"))
	line := dir + "/alp"
	pos := len(line)
	_, _, ok := pathComplete(line, pos, completeKey)
	// alpha and alpine both match "alp"; common prefix is "alp" — already
	// there, so callback signals no extension.
	if ok {
		t.Error("expected ok=false when common prefix equals existing prefix")
	}
}

func TestPathCompleteAppendsSlashForDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	line := dir + "/sub"
	pos := len(line)
	newLine, _, ok := pathComplete(line, pos, completeKey)
	if !ok {
		t.Fatal("expected completion")
	}
	want := dir + "/subdir/"
	if newLine != want {
		t.Errorf("line = %q, want %q", newLine, want)
	}
}

func TestPathCompleteNoMatch(t *testing.T) {
	dir := t.TempDir()
	mustCreate(t, filepath.Join(dir, "alpha"))

	line := dir + "/zz"
	pos := len(line)
	_, _, ok := pathComplete(line, pos, completeKey)
	if ok {
		t.Error("expected no completion, got ok=true")
	}
}

func TestPathCompleteIgnoresNonTabKeys(t *testing.T) {
	_, _, ok := pathComplete("anything", 0, 'a')
	if ok {
		t.Error("expected non-tab key to be ignored")
	}
}

func mustCreate(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
