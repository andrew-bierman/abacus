package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

const checkedInSchemas = "../../docs/schema/v1"

func TestSchemaCommandListsPrintsAndWrites(t *testing.T) {
	code, out, _ := run(t, nil, "schema")
	if code != 0 {
		t.Fatal(code)
	}
	for _, s := range core.SchemaSpecs() {
		if !strings.Contains(out, s.Name) {
			t.Errorf("the list lacks %s:\n%s", s.Name, out)
		}
	}
	code, out, _ = run(t, nil, "schema", "--json")
	var idx core.SchemaIndex
	if code != 0 || json.Unmarshal([]byte(out), &idx) != nil || len(idx.Schemas) != len(core.SchemaSpecs()) {
		t.Fatalf("%d %s", code, out)
	}
	checkContract(t, "schema-index", out)

	want, err := os.ReadFile(filepath.Join(checkedInSchemas, "write-dry-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(t, nil, "schema", "write-dry-run"); code != 0 || out != string(want) {
		t.Errorf("`schema NAME` prints the checked-in file: %d\n%s", code, out)
	}
	dir := filepath.Join(t.TempDir(), "nested", "out")
	if code, out, e := run(t, nil, "schema", "--out", dir); code != 0 || !strings.Contains(out, fmt.Sprintf("wrote %d schemas", len(core.SchemaSpecs()))) {
		t.Fatalf("%d %q %q", code, out, e)
	}
	for _, s := range core.SchemaSpecs() {
		got, err := os.ReadFile(filepath.Join(dir, s.Name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		checked, _ := os.ReadFile(filepath.Join(checkedInSchemas, s.Name+".json"))
		if !bytes.Equal(got, checked) {
			t.Errorf("--out %s differs from docs/schema/v1", s.Name)
		}
	}
	for _, args := range [][]string{{"schema", "nosuch"}, {"schema", "tasks", "board"}, {"schema", "tasks", "--out", dir}, {"schema", "--nope"}} {
		if code, out, _ := run(t, nil, args...); code != 2 || out != "" {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
}

// goldenSchema maps each contract golden to the schema that must describe it.
var goldenSchema = map[string]string{
	"version": "report", "doctor": "report", "status": "report", "board": "board", "tasks": "tasks", "leases": "leases",
	"queues": "queues", "agents": "agents", "search": "search", "config-show": "config-show", "setup": "setup",
	"setup-applied": "setup", "dry-run-bulk": "bulk-dry-run", "applied-bulk": "bulk-result", "applied-update": "write-applied-task",
	"applied-board-post": "write-applied-board", "error-usage": "error", "error-not-connected": "error",
	"error-unknown-outcome": "error", "schema-index": "schema-index",
}

func init() {
	for _, n := range []string{"create", "update", "close", "reopen", "label", "assign", "priority", "dep", "comment", "board-post", "board-reply"} {
		goldenSchema["dry-run-"+n] = "write-dry-run"
	}
}

// Every golden a client decodes validates against the schema made from the Go types: a golden with a key
// the schema lacks (the schema is incomplete), a missing required key, or a wrong type fails here.
func TestContractGoldensValidateAgainstTheSchemas(t *testing.T) {
	schemas := map[string]map[string]any{}
	for _, s := range core.SchemaSpecs() {
		doc, err := core.SchemaDoc(s)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(doc) // the numbers and slices the way a client reads the file
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		schemas[s.Name] = m
	}
	files := map[string]string{}
	for g, s := range goldenSchema {
		files[filepath.Join("testdata", "contract", g+".golden.json")] = s
	}
	for g, s := range map[string]string{"tasks-list": "tasks-list", "tasks-tree": "tasks-tree", "tasks-show": "tasks-show", "tasks-board": "tasks-board",
		"tasks-graph": "tasks-graph", "tasks-graph-whole": "tasks-graph", "search": "search", "search-excerpt": "search", "search-limit": "search", "status-no-workspace": "report"} {
		files[filepath.Join("testdata", g+".golden.json")] = s
	}
	covered := map[string]bool{}
	var names []string
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		sc := schemas[files[f]]
		covered[files[f]] = true
		for _, e := range validate(sc, sc, v, "$") {
			t.Errorf("%s against %s: %s", f, files[f], e)
		}
	}
	// the contract goldens directory has no file this table does not know
	ents, _ := os.ReadDir(filepath.Join("testdata", "contract"))
	for _, e := range ents {
		if _, ok := goldenSchema[strings.TrimSuffix(e.Name(), ".golden.json")]; !ok {
			t.Errorf("%s has no schema in goldenSchema", e.Name())
		}
	}
	for _, s := range core.SchemaSpecs() {
		if !covered[s.Name] && s.Name != "tasks-history" && s.Name != "setup-repair" {
			t.Errorf("no golden validates against the %s schema", s.Name)
		}
	}
}

// A schema that accepted anything would pass the test above, so the validator is itself tested.
func TestSchemaValidatorRejectsWhatIsWrong(t *testing.T) {
	var sc map[string]any
	b, _ := os.ReadFile(filepath.Join(checkedInSchemas, "write-dry-run.json"))
	_ = json.Unmarshal(b, &sc)
	good := `{"schema_version":1,"dry_run":true,"change":["a"],"argv":["bd"],"plan_digest":"x"}`
	for name, c := range map[string]struct {
		doc  string
		fail bool
	}{
		"good":          {good, false},
		"extra context": {strings.Replace(good, `"argv"`, `"context":["c"],"argv"`, 1), false},
		"missing key":   {`{"schema_version":1,"dry_run":true,"change":["a"],"argv":["bd"]}`, true},
		"wrong type":    {strings.Replace(good, `"dry_run":true`, `"dry_run":"yes"`, 1), true},
		"null list":     {strings.Replace(good, `"argv":["bd"]`, `"argv":null`, 1), false}, // a nil Go slice encodes null: lists accept null and [] alike
		"null string":   {strings.Replace(good, `"plan_digest":"x"`, `"plan_digest":null`, 1), true},
		"unknown key":   {strings.Replace(good, `"argv"`, `"surprise":1,"argv"`, 1), true},
		"wrong version": {strings.Replace(good, `"schema_version":1`, `"schema_version":2`, 1), true},
		"list of ints":  {strings.Replace(good, `"argv":["bd"]`, `"argv":[1]`, 1), true},
	} {
		var v any
		_ = json.Unmarshal([]byte(c.doc), &v)
		if got := len(validate(sc, sc, v, "$")) > 0; got != c.fail {
			t.Errorf("%s: rejected=%v, want %v", name, got, c.fail)
		}
	}
}

// validate checks v against the subset of JSON Schema the generator emits: type, const, properties,
// required, items, additionalProperties, anyOf, $ref and the date-time format. A key the schema does
// not name is an error here (the schemas are open for clients, but a golden must be fully described).
func validate(root, s map[string]any, v any, path string) []string {
	if ref, ok := s["$ref"].(string); ok {
		return validate(root, root["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any), v, path)
	}
	if alts, ok := s["anyOf"].([]any); ok {
		for _, alt := range alts {
			if len(validate(root, alt.(map[string]any), v, path)) == 0 {
				return nil
			}
		}
		return []string{path + ": matches none of the alternatives"}
	}
	if c, ok := s["const"]; ok {
		if f, isNum := v.(float64); !isNum || f != c.(float64) {
			return []string{fmt.Sprintf("%s: %v is not the constant %v", path, v, c)}
		}
		return nil
	}
	types := []string{}
	switch t := s["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, x := range t {
			types = append(types, x.(string))
		}
	}
	if len(types) == 0 {
		return nil // no constraint (any)
	}
	var errs []string
	matched := ""
	for _, ty := range types {
		switch ty {
		case "null":
			if v == nil {
				matched = ty
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				matched = ty
			}
		case "string":
			if str, ok := v.(string); ok {
				matched = ty
				if s["format"] == "date-time" {
					if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
						errs = append(errs, path+": not a date-time: "+str)
					}
				}
			}
		case "integer":
			if f, ok := v.(float64); ok && f == float64(int64(f)) {
				matched = ty
			}
		case "number":
			if _, ok := v.(float64); ok {
				matched = ty
			}
		case "array":
			if arr, ok := v.([]any); ok {
				matched = ty
				for i, x := range arr {
					errs = append(errs, validate(root, s["items"].(map[string]any), x, fmt.Sprintf("%s[%d]", path, i))...)
				}
			}
		case "object":
			obj, ok := v.(map[string]any)
			if !ok {
				continue
			}
			matched = ty
			props, _ := s["properties"].(map[string]any)
			add, _ := s["additionalProperties"].(map[string]any)
			for k, x := range obj {
				switch {
				case props[k] != nil:
					errs = append(errs, validate(root, props[k].(map[string]any), x, path+"."+k)...)
				case add != nil:
					errs = append(errs, validate(root, add, x, path+"."+k)...)
				default:
					errs = append(errs, fmt.Sprintf("%s.%s: the schema does not describe this key", path, k))
				}
			}
			if req, ok := s["required"].([]any); ok {
				for _, r := range req {
					if _, has := obj[r.(string)]; !has {
						errs = append(errs, fmt.Sprintf("%s: missing required %s", path, r))
					}
				}
			}
		}
		if matched != "" {
			break
		}
	}
	if matched == "" {
		return []string{fmt.Sprintf("%s: %T is not %v", path, v, types)}
	}
	return errs
}
