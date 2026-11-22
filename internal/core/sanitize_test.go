package core_test

import (
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestSanitizeDropsControlCharactersAndKeepsText(t *testing.T) {
	cases := map[string]string{
		"plain title":                    "plain title",
		"ok\x1b]0;pwned\x07":             "ok]0;pwned",
		"red\x1b[31mtext\x1b[0m":         "red[31mtext[0m",
		"tab\tand\nnewline\rcr":          "tab and newline cr",
		"c1\u009bcontrol":                "c1control",
		"unicode ok: café 日本 \U0001F980": "unicode ok: café 日本 \U0001F980",
		"":                               "",
	}
	for in, want := range cases {
		if got := core.Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Bidi overrides and zero-width characters can disguise or reorder text on screen. ZWJ is
// stripped too, so a ZWJ emoji sequence degrades to its parts rather than being preserved.
func TestSanitizeStripsBidiAndZeroWidthCharacters(t *testing.T) {
	cases := map[string]string{
		"evil\u202etxt.exe":                 "eviltxt.exe",
		"a\u202a\u202b\u202c\u202db":        "ab",
		"iso\u2066x\u2067y\u2068z\u2069":    "isoxyz",
		"zero\u200bwidth\u200c\u200d\ufeff": "zerowidth",
		"lrm\u200e\u200frlm":                "lrmrlm",
		"family \U0001F468\u200d\U0001F469": "family \U0001F468\U0001F469",
		"e\u0301 combining kept":            "e\u0301 combining kept",
	}
	for in, want := range cases {
		if got := core.Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
