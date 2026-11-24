package core

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var updateSchema = flag.Bool("update-schema", false, "rewrite docs/schema/v1 from the Go types")

const schemaDir = "../../docs/schema/v1"

// The checked-in schema is the contract a client generates its types from: a Go type that changes without
// the schema changing (go test ./internal/core -update-schema, then review the diff) fails here.
func TestCheckedInSchemaMatchesTheGoTypes(t *testing.T) {
	want := map[string]bool{}
	for _, s := range SchemaSpecs() {
		b, err := SchemaJSON(s)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(schemaDir, s.Name+".json")
		want[s.Name+".json"] = true
		if *updateSchema {
			if err := os.MkdirAll(schemaDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (go test ./internal/core -update-schema writes it)", err)
		}
		if string(got) != string(b) {
			t.Errorf("docs/schema/v1/%s.json is out of date with the Go type %s (go test ./internal/core -update-schema, then review the diff: only additions are allowed within schema_version 1)", s.Name, s.Type)
		}
	}
	ents, err := os.ReadDir(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !want[e.Name()] && !*updateSchema {
			t.Errorf("docs/schema/v1/%s is not a schema of any spec: delete it", e.Name())
		}
	}
}

type sHeader struct {
	Domain string `json:"domain"`
	State  string `json:"state"`
}
type sLeaf struct {
	N int `json:"n"`
}
type sPlan struct {
	Rungs []string `json:"rungs"`
}

func (sPlan) MarshalJSON() ([]byte, error) { return nil, nil }

type sRoot struct {
	sHeader
	Name    string            `json:"name"`
	Opt     string            `json:"opt,omitempty"`
	Skipped string            `json:"-"`
	hidden  string            //nolint:unused
	List    []string          `json:"list"`
	OptList []string          `json:"opt_list,omitempty"`
	Ptr     *int              `json:"ptr"`
	OptPtr  *float64          `json:"opt_ptr,omitempty"`
	At      time.Time         `json:"at"`
	Leaf    sLeaf             `json:"leaf"`
	Leaves  []sLeaf           `json:"leaves"`
	LeafPtr *sLeaf            `json:"leaf_ptr,omitempty"`
	M       map[string]string `json:"m,omitempty"`
	Any     any               `json:"any"`
	Plan    sPlan             `json:"plan"`
	Self    *sRoot            `json:"self,omitempty"`
}

func TestSchemaFollowsEncodingJSON(t *testing.T) {
	_ = sRoot{}.hidden
	doc, err := SchemaDoc(SchemaSpec{Name: "x", Commands: "x", Type: reflect.TypeFor[sRoot]()})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	var d struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       map[string]struct {
			Required []string `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	prop := func(k string) string { return string(d.Properties[k]) }
	for _, k := range []string{"Skipped", "hidden", "sHeader"} {
		if _, ok := d.Properties[k]; ok {
			t.Errorf("%s must not be a property", k)
		}
	}
	for k, want := range map[string]string{
		"domain":         `{"type":"string"}`, // the embedded header is flattened
		"state":          `{"type":"string"}`,
		"list":           `{"items":{"type":"string"},"type":["array","null"]}`,
		"opt_list":       `{"items":{"type":"string"},"type":"array"}`,
		"ptr":            `{"type":["integer","null"]}`,
		"leaf_ptr":       `{"$ref":"#/$defs/sLeaf"}`,
		"opt_ptr":        `{"type":"number"}`,
		"at":             `{"format":"date-time","type":"string"}`,
		"leaf":           `{"$ref":"#/$defs/sLeaf"}`,
		"leaves":         `{"items":{"$ref":"#/$defs/sLeaf"},"type":["array","null"]}`,
		"m":              `{"additionalProperties":{"type":"string"},"type":"object"}`,
		"any":            `{}`,
		"schema_version": `{"const":1}`,
	} {
		if got := prop(k); got != want {
			t.Errorf("%s: %s, want %s", k, got, want)
		}
	}
	req := strings.Join(d.Required, ",")
	for _, k := range []string{"domain", "name", "list", "at", "leaf", "ptr", "schema_version"} {
		if !strings.Contains(","+req+",", ","+k+",") {
			t.Errorf("%s must be required: %s", k, req)
		}
	}
	for _, k := range []string{"opt", "opt_list", "opt_ptr", "leaf_ptr", "self", "m"} {
		if strings.Contains(","+req+",", ","+k+",") {
			t.Errorf("%s is omitempty, so optional: %s", k, req)
		}
	}
	// a type with its own marshaler forces its lists non-null
	if def := string(mustRaw(t, doc, "$defs", "sPlan", "properties", "rungs")); def != `{"items":{"type":"string"},"type":"array"}` {
		t.Errorf("sPlan.rungs: %s", def)
	}
	// a type that contains itself terminates
	if _, ok := d.Defs["sRoot"]; !ok {
		t.Error("the recursive type is defined once under $defs")
	}
}

func mustRaw(t *testing.T, doc map[string]any, path ...string) []byte {
	t.Helper()
	var v any = doc
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v: not an object at %s", path, p)
		}
		if v, ok = m[p]; !ok {
			t.Fatalf("%v: no %s", path, p)
		}
	}
	b, _ := json.Marshal(v)
	return b
}

func TestEverySpecHasAUniqueNameAndSchemaVersion(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range SchemaSpecs() {
		if seen[s.Name] {
			t.Errorf("duplicate spec %s", s.Name)
		}
		seen[s.Name] = true
		doc, err := SchemaDoc(s)
		if err != nil {
			t.Fatal(err)
		}
		if string(mustRaw(t, doc, "properties", "schema_version")) != `{"const":1}` {
			t.Errorf("%s: schema_version", s.Name)
		}
	}
}
