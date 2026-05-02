package shell

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const completeKey = '\t'

// pathComplete is the AutoCompleteCallback for term.Terminal. It expands the
// word at the cursor against the filesystem when Tab is pressed: single match
// completes inline, multiple matches expand to their longest common prefix,
// no match leaves the line unchanged. Directories are completed with a
// trailing slash so the next Tab descends.
func pathComplete(line string, pos int, key rune) (string, int, bool) {
	if key != completeKey {
		return "", 0, false
	}
	start := wordStart(line, pos)
	word := line[start:pos]

	dir, prefix := splitWord(word)
	matches := readMatches(dir, prefix)
	if len(matches) == 0 {
		return "", 0, false
	}

	completion := commonPrefix(matches)
	if len(matches) == 1 {
		// single match: replace the word, append "/" if dir
		full := filepath.Join(dir, completion)
		if info, err := os.Stat(full); err == nil && info.IsDir() {
			completion += "/"
		}
	}
	if completion == prefix {
		return "", 0, false
	}

	newWord := joinWord(dir, completion, word)
	newLine := line[:start] + newWord + line[pos:]
	return newLine, start + len(newWord), true
}

// wordStart returns the byte index where the current word begins. Words are
// separated by ASCII whitespace; quoting is intentionally not handled — Tab
// completion in the middle of a quoted string just completes against the
// remainder.
func wordStart(line string, pos int) int {
	for i := pos - 1; i >= 0; i-- {
		switch line[i] {
		case ' ', '\t':
			return i + 1
		}
	}
	return 0
}

// splitWord separates a partial path into a directory to scan and the prefix
// to filter by. The directory is what os.ReadDir is called on; the prefix is
// matched against entry names.
func splitWord(word string) (dir, prefix string) {
	i := strings.LastIndex(word, "/")
	if i < 0 {
		return ".", word
	}
	return word[:i+1], word[i+1:]
}

// joinWord rebuilds the completed word, preserving the original directory
// portion of word so that "/foo/bar" + completion stays absolute.
func joinWord(dir, completion, original string) string {
	i := strings.LastIndex(original, "/")
	if i < 0 {
		_ = dir
		return completion
	}
	return original[:i+1] + completion
}

func readMatches(dir, prefix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var matches []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	return matches
}

func commonPrefix(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	prefix := matches[0]
	for _, m := range matches[1:] {
		for !strings.HasPrefix(m, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}
