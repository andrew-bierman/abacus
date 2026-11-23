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

// Beyond bidi and zero-width space: word joiner and the invisible operators, soft hyphen, the
// Arabic letter mark, Mongolian vowel separator, deprecated format characters, Hangul fillers,
// the combining grapheme joiner, interlinear annotation and tag characters all print as nothing
// (or reorder text) and are dropped. Visible text around them is kept.
func TestSanitizeStripsEveryInvisibleFormatCharacter(t *testing.T) {
	for name, r := range map[string]rune{
		"word joiner": 0x2060, "function application": 0x2061, "invisible plus": 0x2064, "soft hyphen": 0x00ad, "arabic letter mark": 0x061c,
		"mongolian vowel separator": 0x180e, "deprecated shaping": 0x206a, "deprecated digit shape": 0x206f, "hangul choseong filler": 0x115f,
		"hangul jungseong filler": 0x1160, "hangul filler": 0x3164, "halfwidth hangul filler": 0xffa0, "grapheme joiner": 0x034f,
		"interlinear anchor": 0xfff9, "interlinear terminator": 0xfffb, "tag space": 0xe0020, "tag latin a": 0xe0041, "cancel tag": 0xe007f,
		"bom": 0xfeff, "zwsp": 0x200b, "zwj": 0x200d, "lrm": 0x200e, "rlo": 0x202e, "isolate": 0x2066, "pop isolate": 0x2069,
		"arabic number sign": 0x0600, "left-to-right mark": 0x200e,
	} {
		in := "a" + string(r) + "b"
		if got := core.Sanitize(in); got != "ab" {
			t.Errorf("%s (%U): %q", name, r, got)
		}
		if got := core.SanitizeText("x\n" + in); got != "x\nab" {
			t.Errorf("%s (%U) in text: %q", name, r, got)
		}
	}
	for _, keep := range []string{"日本語", "ünï", "é", "😀", "·", "–", "©", " ", "a b", "ﬁ"} {
		if got := core.Sanitize(keep); got != keep {
			t.Errorf("%q was changed to %q", keep, got)
		}
	}
}
