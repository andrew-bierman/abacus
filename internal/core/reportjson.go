package core

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// ReportJSON is a typed report as `--json` prints it: indented, with schema_version added. The CLI
// and the TUI's JSON panel both call it, so a panel shows exactly what the command line would.
func ReportJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["schema_version"] = json.RawMessage(strconv.Itoa(SchemaVersion))
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
