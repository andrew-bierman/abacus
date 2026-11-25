package core

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// ReportJSON is a typed report as `--json` prints it: indented, with schema_version added. The CLI
// and the TUI's JSON panel both call it, so a panel shows exactly what the command line would.
func ReportJSON(v any) ([]byte, error) { return reportJSON(v, false) }

// DemoReportJSON is ReportJSON for a document made from demo fixtures: it also carries "demo": true.
func DemoReportJSON(v any) ([]byte, error) { return reportJSON(v, true) }

// ReportJSONCompact is ReportJSON on one line with no trailing newline: the same keys in the same
// order, for the serve protocol, where one message is one line. demo adds "demo": true.
func ReportJSONCompact(v any, demo bool) ([]byte, error) {
	m, err := versioned(v, demo)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func reportJSON(v any, demo bool) ([]byte, error) {
	m, err := versioned(v, demo)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func versioned(v any, demo bool) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["schema_version"] = json.RawMessage(strconv.Itoa(SchemaVersion))
	if demo {
		m["demo"] = json.RawMessage("true")
	}
	return m, nil
}
