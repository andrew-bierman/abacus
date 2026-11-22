package bd

import "testing"

func TestReadAllowlistIsExact(t *testing.T) {
	ok := [][]string{
		{"--readonly", "version", "--json"},
		{"--readonly", "status", "--json", "--no-activity"},
		{"--readonly", "list", "--json", "--brief", "--all", "--sort", "updated", "--flat", "-n", "8"},
		{"-C", "/trk", "--readonly", "version", "--json"},
	}
	for _, a := range ok {
		if !readAllowed(a, "/trk") {
			t.Errorf("read should be allowed: %v", a)
		}
	}
	bad := [][]string{
		{"version", "--json"},                 // not readonly
		{"--readonly", "create", "--title=x"}, // a write
		{"--readonly", "list", "--json", "--all"},
		{"-C", "/other", "--readonly", "version", "--json"},
		{"-C", "/trk", "-C", "/trk", "--readonly", "version", "--json"},
		{"--readonly", "sync"},
		{},
	}
	for _, a := range bad {
		if readAllowed(a, "/trk") {
			t.Errorf("read should be refused: %v", a)
		}
	}
	if readAllowed([]string{"-C", "/trk", "--readonly", "version", "--json"}, "") {
		t.Error("-C only with a configured dir")
	}
}

func TestWriteAllowlistIsPerVerbAndExact(t *testing.T) {
	ok := [][]string{
		{"create", "--json", "--title=new thing"},
		{"create", "--json", "--title=x", "--description=a\nb", "--priority=1", "--type=bug"},
		{"update", "tt-1", "--json", "--title=x"},
		{"update", "tt-1", "--json", "--status=in_progress", "--priority=0"},
		{"close", "tt-1", "--json"},
		{"close", "tt-1", "--json", "--reason=done"},
		{"-C", "/trk", "close", "tt-1", "--json"},
	}
	for _, a := range ok {
		if !writeAllowed(a, "/trk") && !(a[0] != "-C" && writeAllowed(a, "")) {
			t.Errorf("write should be allowed: %v", a)
		}
	}
	bad := [][]string{
		{"create", "--json"},                         // no title
		{"create", "--json", "--title=x", "--force"}, // unlisted flag
		{"create", "--json", "--title=x", "--priority=9"},
		{"create", "--json", "--title=x", "--type=bogus"},
		{"create", "--json", "--title=a", "--title=b"},  // duplicate
		{"create", "--json", "--title", "x"},            // value as separate arg
		{"create", "--title=x"},                         // no --json
		{"update", "--force", "--json", "--title=x"},    // flag-like id
		{"update", "tt-1", "--json"},                    // nothing to change
		{"update", "tt-1", "--json", "--status=closed"}, // closing is close's job
		{"update", "tt-1", "--json", "--claim"},
		{"close", "tt-1", "--json", "--force"},
		{"close", "tt-1", "tt-2", "--json"}, // one id at a time
		{"close", "--json"},
		{"delete", "tt-1", "--json"},
		{"reopen", "tt-1", "--json"},
		{"sync"},
		{"-C", "/other", "close", "tt-1", "--json"},
		{},
	}
	for _, a := range bad {
		if writeAllowed(a, "") || writeAllowed(a, "/trk") {
			t.Errorf("write should be refused: %v", a)
		}
	}
	if writeAllowed([]string{"-C", "/trk", "close", "tt-1", "--json"}, "") {
		t.Error("-C only with a configured dir")
	}
}

func TestReadAndWriteAllowlistsAreDisjoint(t *testing.T) {
	if writeAllowed([]string{"--readonly", "list", "--json"}, "") || readAllowed([]string{"close", "tt-1", "--json"}, "") {
		t.Fatal("a read argv must not pass the write list and vice versa")
	}
}
