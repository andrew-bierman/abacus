package core

import (
	"errors"
	"strings"
	"testing"
)

func TestReadDoctorUnparsableOrFailedToRunIsFail(t *testing.T) {
	for name, c := range map[string]struct {
		out  string
		exit int
		err  error
	}{
		"not json": {out: "oops"}, "empty output": {}, "object": {out: `{"a":1}`}, "runner error": {err: errors.New("boom")},
	} {
		got := ReadDoctor("tool", []byte(c.out), c.exit, c.err)
		if len(got) != 1 || got[0].Status != StatusFail || !strings.Contains(got[0].Name, "tool") {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestReadDoctorEmptyReportFailsOnNonZeroExitWarnsOnZero(t *testing.T) {
	for _, body := range []string{"[]", "null"} {
		if got := ReadDoctor("tool", []byte(body), 0, nil); len(got) != 1 || got[0].Status != StatusWarn {
			t.Errorf("%s exit 0: %+v", body, got)
		}
		if got := ReadDoctor("tool", []byte(body), 64, nil); len(got) != 1 || got[0].Status != StatusFail {
			t.Errorf("%s exit 64 must fail, not warn: %+v", body, got)
		}
	}
}

func TestReadDoctorBackstopNormalizeAndSanitize(t *testing.T) {
	body := `[{"name":"a\u001b[2J","status":"WEIRD","detail":"d\u001b]0;x\u0007","fix":"f\u001b[31m"}]`
	got := ReadDoctor("tool", []byte(body), 1, nil)
	if len(got) != 2 || got[0].Status != StatusWarn || got[1].Status != StatusFail || !strings.Contains(got[1].Detail, "1") {
		t.Fatalf("want [warn, synthetic fail]: %+v", got)
	}
	for _, c := range got {
		for _, r := range c.Name + c.Detail + c.Fix {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Fatalf("control rune %U in %+v", r, c)
			}
		}
	}
	// A real failing check is not duplicated, and exit 0 adds nothing.
	if got := ReadDoctor("tool", []byte(`[{"name":"x","status":"fail"}]`), 1, nil); len(got) != 1 {
		t.Fatalf("duplicated: %+v", got)
	}
	if got := ReadDoctor("tool", []byte(`[{"name":"x","status":"ok"}]`), 0, nil); len(got) != 1 || got[0].Status != StatusOK {
		t.Fatalf("%+v", got)
	}
}
