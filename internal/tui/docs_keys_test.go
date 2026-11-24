package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every key the TUI binds is named in docs/tui.md, so a new binding fails this test until the key table
// says what it does. The keys are read from the key.WithKeys(...) calls in the package sources.
func TestEveryBoundKeyIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "tui.md"))
	if err != nil {
		t.Fatal(err)
	}
	// a documented key is a code span (`x`), possibly one of several in the span's text: "`h` `l`", "`ctrl+s`"
	spans := map[string]bool{}
	prose := regexp.MustCompile("(?s)```.*?```").ReplaceAllString(string(doc), "")
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(prose, -1) {
		spans[m[1]] = true
		for _, f := range strings.Fields(strings.NewReplacer("/", " ", ",", " ").Replace(m[1])) {
			spans[f] = true
		}
	}
	files, _ := filepath.Glob("*.go")
	call := regexp.MustCompile(`key\.WithKeys\(([^)]*)\)`)
	quoted := regexp.MustCompile(`"([^"]+)"`)
	missing := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range call.FindAllStringSubmatch(string(src), -1) {
			for _, k := range quoted.FindAllStringSubmatch(c[1], -1) {
				if name := k[1]; !spans[name] && !spans[alias[name]] && !(len(name) == 1 && name >= "1" && name <= "6" && spans["1"] && spans["6"]) {
					missing[name] = f
				}
			}
		}
	}
	var names []string
	for k := range missing {
		names = append(names, k+" ("+missing[k]+")")
	}
	sort.Strings(names)
	if len(names) > 0 {
		t.Errorf("keys bound in the TUI but not in docs/tui.md: %s", strings.Join(names, ", "))
	}
}

// alias maps a key's internal name to how the docs write it.
// (a digit 1-6 is covered by the documented range `1`-`6`).
var alias = map[string]string{"pgdown": "pgdn", " ": "space"}
