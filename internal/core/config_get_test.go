package core_test

import (
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Every settable key can be read back, search_history reads "on" unless it is "off", and an
// unknown key is an error rather than an empty value.
func TestConfigGetReadsEveryKnownKey(t *testing.T) {
	c := core.Config{Actor: "a", TrackerDir: "/t", LambeConfig: "/l.yaml", BoardURL: "https://b.example"}
	want := map[string]string{"actor": "a", "tracker_dir": "/t", "lambe_config": "/l.yaml", "board_url": "https://b.example", "search_history": "on"}
	if len(want) != len(core.ConfigKeys) {
		t.Fatalf("ConfigKeys has %v; update this test with the new key", core.ConfigKeys)
	}
	for _, k := range core.ConfigKeys {
		got, err := c.Get(k)
		if err != nil || got != want[k] {
			t.Errorf("Get(%q) = %q, %v; want %q", k, got, err, want[k])
		}
	}
	c.SearchHistory = "off"
	if got, _ := c.Get("search_history"); got != "off" {
		t.Errorf("off must read back as off, got %q", got)
	}
	if _, err := c.Get("nope"); err == nil {
		t.Error("an unknown key must be an error")
	}
}
