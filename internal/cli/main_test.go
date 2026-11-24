package cli_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

var (
	usedMu      sync.Mutex
	usedGolden  = map[string]bool{}
	contractDir = filepath.Join("testdata", "contract")
)

// See internal/app/main_test.go. After a whole run, a contract golden no test produced is stale: -update-golden
// deletes it, a plain run fails (a renamed or removed case must not leave its old bytes behind as "the contract").
func TestMain(m *testing.M) {
	core.SetEmbeddedForTest(nil)
	code := m.Run()
	if (code == 0 || *updateGolden) && flag.Lookup("test.run").Value.String() == "" {
		ents, _ := os.ReadDir(contractDir)
		for _, e := range ents {
			name := strings.TrimSuffix(e.Name(), ".golden.json")
			if usedGolden[name] {
				continue
			}
			if *updateGolden {
				_ = os.Remove(filepath.Join(contractDir, e.Name()))
				fmt.Fprintf(os.Stderr, "removed stale golden %s\n", e.Name())
			} else {
				fmt.Fprintf(os.Stderr, "stale contract golden %s: no test produces it (go test ./internal/cli -update-golden removes it)\n", e.Name())
				code = 1
			}
		}
	}
	os.Exit(code)
}
