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
