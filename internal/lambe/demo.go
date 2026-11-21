package lambe

import (
	"embed"
	"encoding/json"
)

// The demo reads the vendored contract fixtures: no daemon, tool or network involved.
//
//go:embed testdata/contract/v1/*.json
var fixtures embed.FS

// NewDemo returns a Lambe module backed entirely by fixtures.
func NewDemo() *Module { return New(Options{Demo: true}) }

func demoView() (View, error) {
	var v View
	for name, dst := range map[string]any{"snapshot.json": &v.Snapshot, "queue.json": &v.Queue} {
		b, err := fixtures.ReadFile("testdata/contract/v1/" + name)
		if err != nil {
			return View{}, err
		}
		if err := json.Unmarshal(b, dst); err != nil {
			return View{}, err
		}
	}
	return v, nil
}
