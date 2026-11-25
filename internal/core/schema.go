package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// The contract schema: a JSON Schema (draft 2020-12) for every report and write envelope, derived by
// reflection from the Go types the CLI encodes, so a client's types can be generated from it and a
// change to a Go type cannot go unnoticed (a test compares it with the checked-in docs/schema/v1).
//
// It follows encoding/json: field names and omitempty from the tags, embedded structs flattened,
// unexported and json:"-" fields left out, a nil slice or map or pointer may be null unless the
// field is omitempty, time.Time is a date-time string. A type with its own MarshalJSON (OpenPlan,
// OpenRung) forces its lists non-null. Closed sets that may grow (states, kinds, statuses) are plain
// strings: a new value is additive, so a schema enum would break a client that generates one.
// A string type that implements SchemaEnum is the exception: it is a closed set the contract promises not to
// grow within schema_version 1, so its schema lists the values as an enum.
// The schemas are open (no additionalProperties: false) because a new field is additive too.
// schema_version is added to every root, as ReportJSON adds it to the output, and so is the optional demo flag.

// SchemaSpec names one schema: the Go type, and the commands whose --json output it describes.
type SchemaSpec struct {
	Name     string
	Commands string
	Type     reflect.Type
}

func typeOf[T any]() reflect.Type { return reflect.TypeFor[T]() }

// SchemaSpecs is every exported schema, in documentation order.
func SchemaSpecs() []SchemaSpec {
	return []SchemaSpec{
		{"report", "version, doctor, status", typeOf[Report]()},
		{"board", "board", typeOf[BoardReport]()},
		{"tasks", "tasks", typeOf[TasksReport]()},
		{"leases", "leases", typeOf[LeasesReport]()},
		{"queues", "queues", typeOf[QueuesReport]()},
		{"agents", "agents", typeOf[AgentsReport]()},
		{"search", "search", typeOf[SearchReport]()},
		{"tasks-list", "tasks list", typeOf[TaskListReport]()},
		{"tasks-tree", "tasks list --tree", typeOf[TaskTreeReport]()},
		{"tasks-show", "tasks show", typeOf[TaskDetailReport]()},
		{"tasks-board", "tasks board", typeOf[KanbanReport]()},
		{"tasks-graph", "tasks graph", typeOf[GraphView]()},
		{"tasks-history", "tasks history", typeOf[TaskHistoryReport]()},
		{"serve-tasks-since", "serve tasks.since", typeOf[TasksSince]()},
		{"setup", "setup", typeOf[SetupReport]()},
		{"setup-repair", "setup --repair", typeOf[SetupRepairReport]()},
		{"config-show", "config show", typeOf[ConfigShow]()},
		{"write-dry-run", "every single write with --dry-run", typeOf[WriteDryRun]()},
		{"write-applied-task", "every tasks write with --yes", typeOf[TaskApplied]()},
		{"write-applied-board", "board post and reply with --yes", typeOf[BoardApplied]()},
		{"bulk-dry-run", "tasks bulk --dry-run", typeOf[BulkDryRun]()},
		{"bulk-result", "tasks bulk --yes", typeOf[BulkReport]()},
		{"workspace", "status (domains.workspace), serve workspace", typeOf[WorkspaceReport]()},
		{"serve-initialize", "serve initialize", typeOf[ServeInitialize]()},
		{"serve-plan", "serve plan.* methods", typeOf[ServePlan]()},
		{"serve-applied", "serve apply", typeOf[ServeApplied]()},
		{"serve-error", "serve error.data (the error object plus the plan it concerns; schema_version is not sent there)", typeOf[ServeErrorData]()},
		{"error", "any command that fails, with --json", typeOf[ErrorDoc]()},
		{"schema-index", "schema --json", typeOf[SchemaIndex]()},
	}
}

// SchemaEnum is implemented by a string type that is a closed set of values.
type SchemaEnum interface{ SchemaValues() []string }

// SchemaDoc is the JSON Schema of one spec.
func SchemaDoc(s SchemaSpec) (map[string]any, error) {
	g := &schemaGen{defs: map[string]map[string]any{}, owner: map[string]reflect.Type{}}
	root, err := g.object(s.Type, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Name, err)
	}
	props := root["properties"].(map[string]any)
	props["schema_version"] = map[string]any{"const": SchemaVersion}
	props["demo"] = map[string]any{"type": "boolean", "description": "true when every value is invented fixture data (--demo, ABACUS_DEMO=1); omitted otherwise"}
	req := append(stringsOf(root["required"]), "schema_version")
	sort.Strings(req)
	root["required"] = dedup(req)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["x-abacus-schema"] = SchemaVersion // marks the file as one abacus wrote, so --out only ever replaces its own
	root["title"] = s.Name
	root["description"] = "The --json output of: " + s.Commands
	if s.Type == typeOf[TasksSince]() {
		root["description"] = "A full task snapshot or a delta from the requested fingerprint."
		props["fingerprint"].(map[string]any)["description"] = "Opaque token for the snapshot used to produce this response."
		props["rows"].(map[string]any)["description"] = "All rows for full, or new and changed rows for delta, sorted by id."
		props["removed"].(map[string]any)["description"] = "IDs removed since the requested fingerprint for delta. Empty for full."
	}
	if len(g.defs) > 0 {
		defs := map[string]any{}
		for k, v := range g.defs {
			defs[k] = v
		}
		root["$defs"] = defs
	}
	return root, nil
}

// SchemaJSON is the schema as it is checked in and printed: indented, sorted keys, a final newline.
func SchemaJSON(s SchemaSpec) ([]byte, error) {
	doc, err := SchemaDoc(s)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n'), err
}

type schemaGen struct {
	defs  map[string]map[string]any
	owner map[string]reflect.Type // def name -> the type that claimed it (two types with one name is an error)
}

var (
	timeType      = reflect.TypeFor[time.Time]()
	rawType       = reflect.TypeFor[json.RawMessage]()
	marshalerType = reflect.TypeFor[json.Marshaler]()
)

// object is the schema of struct t, with its fields flattened the way encoding/json flattens them.
func (g *schemaGen) object(t reflect.Type, nonNull bool) (map[string]any, error) {
	props := map[string]any{}
	var required []string
	var walk func(t reflect.Type) error
	walk = func(t reflect.Type) error {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" && opts == "" {
				continue
			}
			ft := f.Type
			if f.Anonymous && name == "" {
				et := ft
				if et.Kind() == reflect.Pointer {
					et = et.Elem()
				}
				if et.Kind() == reflect.Struct {
					if err := walk(et); err != nil {
						return err
					}
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			omit := strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")
			if _, dup := props[name]; dup {
				return fmt.Errorf("%s: two fields are named %q in the JSON", t, name)
			}
			s, err := g.schema(ft, !omit && !nonNull)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", t, f.Name, err)
			}
			props[name] = s
			if !omit {
				required = append(required, name)
			}
		}
		return nil
	}
	if err := walk(t); err != nil {
		return nil, err
	}
	sort.Strings(required)
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

// schema is the schema of a value of type t; nullable says a nil value encodes as null.
func (g *schemaGen) schema(t reflect.Type, nullable bool) (map[string]any, error) {
	if t == timeType {
		return typed("string", false, map[string]any{"format": "date-time"}), nil
	}
	if t == rawType {
		return map[string]any{}, nil
	}
	switch t.Kind() {
	case reflect.Pointer: // null only when the field is not omitempty
		s, err := g.schema(t.Elem(), false)
		if err != nil || !nullable {
			return s, err
		}
		return makeNullable(s), nil
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.Bool:
		return typed("boolean", false, nil), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return typed("integer", false, nil), nil
	case reflect.Float32, reflect.Float64:
		return typed("number", false, nil), nil
	case reflect.String:
		if e, ok := reflect.Zero(t).Interface().(SchemaEnum); ok {
			vals := e.SchemaValues()
			enum := make([]any, len(vals))
			for i, v := range vals {
				enum[i] = v
			}
			return typed("string", false, map[string]any{"enum": enum}), nil
		}
		return typed("string", false, nil), nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return typed("string", nullable, map[string]any{"contentEncoding": "base64"}), nil
		}
		items, err := g.schema(t.Elem(), true)
		if err != nil {
			return nil, err
		}
		return typed("array", nullable && t.Kind() == reflect.Slice, map[string]any{"items": items}), nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map with %s keys", t.Key())
		}
		vs, err := g.schema(t.Elem(), true)
		if err != nil {
			return nil, err
		}
		return typed("object", nullable, map[string]any{"additionalProperties": vs}), nil
	case reflect.Struct:
		return g.ref(t)
	}
	return nil, fmt.Errorf("type %s is not describable", t)
}

// ref defines struct t once under $defs and points at it.
func (g *schemaGen) ref(t reflect.Type) (map[string]any, error) {
	name := defName(t)
	if prev, ok := g.owner[name]; ok && prev != t {
		return nil, fmt.Errorf("%s and %s are both called %s", prev, t, name)
	}
	if _, ok := g.owner[name]; !ok {
		g.owner[name] = t
		g.defs[name] = nil // claimed before the walk: a type that contains itself terminates
		def, err := g.object(t, t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType))
		if err != nil {
			return nil, err
		}
		g.defs[name] = def
	}
	return map[string]any{"$ref": "#/$defs/" + name}, nil
}

// makeNullable lets a schema also be null.
func makeNullable(s map[string]any) map[string]any {
	switch k := s["type"].(type) {
	case string:
		s["type"] = []any{k, "null"}
	case nil:
		if _, ok := s["$ref"]; ok {
			return map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}
		}
	}
	return s
}

func defName(t reflect.Type) string { return t.Name() }

func typed(kind string, nullable bool, extra map[string]any) map[string]any {
	m := map[string]any{"type": kind}
	if nullable {
		m["type"] = []any{kind, "null"}
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func stringsOf(v any) []string {
	s, _ := v.([]string)
	return append([]string(nil), s...)
}

func dedup(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
