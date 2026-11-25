package cli

import (
	"os"
	"strings"
	"testing"
)

func TestScrubServeEnvDropsLoaderVariablesAndShellStartupFiles(t *testing.T) {
	for _, k := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "BASH_ENV", "ENV"} {
		t.Setenv(k, "/x")
	}
	t.Setenv("HOME_KEPT", "1")
	t.Setenv("LDAP_URI", "kept") // starts with LD but not LD_
	scrubServeEnv()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "LD_") || name == "BASH_ENV" || name == "ENV" {
			t.Errorf("%s survived", name)
		}
	}
	if os.Getenv("HOME_KEPT") != "1" || os.Getenv("LDAP_URI") != "kept" {
		t.Error("an ordinary variable was dropped")
	}
}

func TestCheckServeStdin(t *testing.T) {
	if err := checkServeStdin(nil); err == nil {
		t.Error("no stdin")
	}
	if err := checkServeStdin(strings.NewReader("")); err != nil { // a plain reader (a test's) is taken at its word
		t.Error(err)
	}
	f, _ := os.Open(os.DevNull)
	defer f.Close()
	if err := checkServeStdin(f); err == nil {
		t.Error("/dev/null is a character device, not a client")
	}
	reg, _ := os.Open("serve.go")
	defer reg.Close()
	if err := checkServeStdin(reg); err == nil {
		t.Error("a regular file is not a client")
	}
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if err := checkServeStdin(r); err != nil {
		t.Errorf("a pipe is: %v", err)
	}
}
