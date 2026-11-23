package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

func licenses(t *testing.T, embedded bool, args ...string) (int, string, string) {
	t.Helper()
	if embedded {
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
	} else {
		t.Cleanup(core.SetEmbeddedForTest(nil))
	}
	var o, e bytes.Buffer
	code := cli.Run(append([]string{"licenses"}, args...), cli.Deps{Out: &o, Err: &e, Version: "t"})
	return code, o.String(), e.String()
}

func TestLicensesPrintsTheNoticesAndEveryEmbeddedLicenseText(t *testing.T) {
	code, out, _ := licenses(t, true)
	for _, want := range []string{"# notices", "bd/LICENSE", "BD LICENSE", "bv/LICENSE", "BV LICENSE"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if code != core.ExitOK {
		t.Fatalf("exit %d", code)
	}
	if strings.Index(out, "# notices") > strings.Index(out, "BD LICENSE") {
		t.Error("the notices come first")
	}
}

func TestLicensesInADevelopmentBuildSaysItCarriesNone(t *testing.T) {
	code, out, _ := licenses(t, false)
	if code != core.ExitOK || !strings.Contains(out, "carries no tool licenses") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestLicensesFailsClosedWhenTheEmbeddedTextIsGone(t *testing.T) {
	set := embedtest.Standard()
	delete(set.FS, "licenses/bd/LICENSE")
	t.Cleanup(core.SetEmbeddedForTest(set.FS))
	var o, e bytes.Buffer
	if code := cli.Run([]string{"licenses"}, cli.Deps{Out: &o, Err: &e}); code == core.ExitOK || !strings.Contains(e.String(), "damaged") {
		t.Fatalf("%d %q", code, e.String())
	}
}

func TestLicensesTakesNoArguments(t *testing.T) {
	if code, _, e := licenses(t, true, "x"); code != core.ExitUsage || !strings.Contains(e, "unexpected argument") {
		t.Fatalf("%d %q", code, e)
	}
}
