package taskview

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

var update = flag.Bool("update", false, "rewrite the *.golden.json files in testdata")

// fixedNow is just after the fixtures were captured, so ages and "ready" are stable.
var fixedNow = time.Date(2026, 11, 23, 4, 20, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func realSnapshot(t *testing.T) core.TaskSnapshot {
	t.Helper()
	s, err := Parse(bytes.NewReader(fixture(t, "export.jsonl")), fixedNow, "tracker")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func realView(t *testing.T) *View { t.Helper(); return Build(realSnapshot(t), fixedNow) }

func ids(rows []core.TaskRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// golden compares v's JSON with testdata/<name>; -update rewrites it.
func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/taskview -update creates it)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs (go test ./internal/taskview -update accepts):\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}
