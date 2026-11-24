package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
)

// bindingOwners reads the non-test source of this package and returns, for each key string, the
// names of the package-level bindings built with it.
func bindingOwners(t *testing.T) map[string][]string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	owners := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, v := range vs.Values {
					ast.Inspect(v, func(n ast.Node) bool {
						c, ok := n.(*ast.CallExpr)
						if !ok {
							return true
						}
						if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithKeys" {
							for _, a := range c.Args {
								if lit, ok := a.(*ast.BasicLit); ok {
									k := strings.Trim(lit.Value, `"`)
									owners[k] = append(owners[k], vs.Names[i].Name)
								}
							}
						}
						return true
					})
				}
			}
		}
	}
	return owners
}

// `:` and ctrl+k have exactly one owner each; d and y are the shell's describe keys, the viewer's
// close keys, and (y) the confirm screen's apply, nothing else.
func TestColonAndCtrlKHaveOneOwnerEach(t *testing.T) {
	o := bindingOwners(t)
	if got := o[":"]; len(got) != 1 || got[0] != "keyPrompt" {
		t.Errorf(": is owned by %v", got)
	}
	if got := o["ctrl+k"]; len(got) != 1 || got[0] != "keySearch" {
		t.Errorf("ctrl+k is owned by %v", got)
	}
	for k, allowed := range map[string][]string{
		"d": {"keyDescribe", "keyPickDelete", "keyViewerClose"}, // keyPickDelete: the saved-filter picker captures every key while open
		"y": {"keyJSON", "keyViewerClose", "keyYes"},
	} {
		got := append([]string(nil), o[k]...)
		sort.Strings(got)
		want := append([]string(nil), allowed...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s is owned by %v, want %v", k, got, want)
		}
	}
	// ctrl+p and ctrl+n are history in whichever modal is open; there are exactly two modals that use them
	for _, k := range []string{"ctrl+p", "ctrl+n"} {
		got := append([]string(nil), o[k]...)
		sort.Strings(got)
		// keyRecall: recent `/` queries while the Tasks filter is being typed, a capturing mode of its own
		if strings.Join(got, ",") != "keyRecall,keySearchHist" {
			t.Errorf("%s is owned by %v (the prompt reads it as a key type, not a binding)", k, got)
		}
	}
}

// The merged key map: on every tab and in every Tasks view the section's own keys never reuse a key
// the shell owns (`:`, ctrl+k, the global keys, and d and y where the section can be described).
func TestShellKeysNeverCollideWithAnySectionKey(t *testing.T) {
	m0 := pm(t, 120, 32).(*Model)
	shell := map[string]string{}
	for name, b := range map[string]key.Binding{"quit": keyQuit, "next": keyNext, "prev": keyPrev, "jump": keyJump, "help": keyHelp, "search": keySearch, "prompt": keyPrompt} {
		for _, k := range b.Keys() {
			shell[k] = name
		}
	}
	setups := map[string][]string{"board": {"1"}, "tasks": {"2"}, "tasks flat": {"2", "T"}, "graph": {"2", "g"}, "kanban": {"2", "v"}, "kanban detail": {"2", "v", "enter"}, "kanban group": {"2", "v", "s"}, "insights": {"2", "V"}, "filtered": {"2", "/", "a", "enter"}, "leases": {"3"}, "queues": {"4"}, "sessions": {"5"}, "doctor": {"6"}}
	for name, keys := range setups {
		for _, w := range []int{60, 120} {
			m := sized(m0, w, 32)
			for _, k := range keys {
				m = press(m, k)
			}
			mm := m.(*Model)
			sec := mm.active()
			if sec.Capturing() {
				continue // the section owns every key: the shell's keys are not offered
			}
			own := map[string]bool{}
			for _, k := range keyStrings(sec.Bindings()) {
				own[k] = true
				if g, clash := shell[k]; clash && k != "esc" {
					t.Errorf("%s %d: the section binds %q, which is the shell's %s key", name, w, k, g)
				}
			}
			if mm.describes() {
				for _, k := range []string{"d", "y"} {
					if own[k] {
						t.Errorf("%s %d: the section binds %q, which describes the selected task", name, w, k)
					}
				}
			}
			// the shell's footer and overlay keys come from the same bindings: no key twice
			seen := map[string]bool{}
			for _, k := range keyStrings(mm.ShortHelp()) {
				if seen[k] && k != "esc" {
					t.Errorf("%s %d: the footer lists %q twice", name, w, k)
				}
				seen[k] = true
			}
		}
	}
}

func TestHelpOverlayListsThePromptCommands(t *testing.T) {
	m := press(pm(t, 100, 40), "?")
	out := strings.Join(lines(m), "\n")
	for _, want := range []string{"Commands", "tasks board leases queues sessions doctor", "list tree kanban graph insights", "search TEXT", "filter QUERY", "ctx NAME", "help", ": ", "command"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	// at 24 rows the overlay scrolls instead of cutting the commands off
	m = press(pm(t, 60, 24), "?")
	first := strings.Join(lines(m), "\n")
	for i := 0; i < 30; i++ {
		m = press(m, "j")
	}
	last := strings.Join(lines(m), "\n")
	if first == last || !strings.Contains(last, "quit") {
		t.Errorf("the help overlay must scroll to its end at 60x24:\n--- first\n%s\n--- last\n%s", first, last)
	}
}
