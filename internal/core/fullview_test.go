package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type recRun struct {
	calls [][]string
	out   []byte
	exit  int
	err   error
}

func (r *recRun) Run(_ context.Context, name string, args ...string) ([]byte, int, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.out, r.exit, r.err
}

func TestProbeFullViewMissingSaysItIsOptionalAndRunsNothing(t *testing.T) {
	r := &recRun{}
	_, err := core.ProbeFullView(context.Background(), func(string) (string, error) { return "", errors.New("nope") }, r)
	if err == nil || !strings.Contains(err.Error(), "optional program") || strings.Contains(err.Error(), "install") || len(r.calls) != 0 {
		t.Fatalf("%v %v", err, r.calls)
	}
}

func TestProbeFullViewRunsOnlyDashVersion(t *testing.T) {
	r := &recRun{}
	bin, err := core.ProbeFullView(context.Background(), func(string) (string, error) { return "/x/b9s", nil }, r)
	if err != nil || bin != "/x/b9s" || len(r.calls) != 1 || strings.Join(r.calls[0], " ") != "/x/b9s -version" {
		t.Fatalf("%v %v %v", bin, err, r.calls)
	}
	r = &recRun{exit: 2}
	if _, err := core.ProbeFullView(context.Background(), func(string) (string, error) { return "/x/b9s", nil }, r); err == nil {
		t.Fatal("a failing probe must be an error")
	}
}

func TestResolveProvidersMatchesDomains(t *testing.T) {
	p := core.ResolveProviders(nil)
	if p.Tasks != nil || p.Writer != nil || p.Sessions != nil || len(p.Missing) != 5 {
		t.Fatalf("%+v", p)
	}
}
