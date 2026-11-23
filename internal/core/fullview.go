package core

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// FullViewName is the ecosystem tool abacus hands off to for the full interactive board,
// tree and graph. It is only ever named in handoff errors.
const FullViewName = "b9s"

// FullViewMissing is shown when the handoff cannot find the tool.
const FullViewMissing = "The full board, tree and graph view opens b9s, a separate optional program that abacus did not find on this computer"

// ProbeFullView finds the full-view tool: it must be on PATH and answer `-version` (the flag
// its -help lists). It runs nothing else. Callers run it only on an explicit user action.
func ProbeFullView(ctx context.Context, look func(string) (string, error), run Runner) (string, error) {
	bin, err := look(FullViewName)
	if err != nil || bin == "" {
		return "", errors.New(FullViewMissing)
	}
	if _, exit, err := run.Run(ctx, bin, "-version"); err != nil || exit != 0 {
		return "", fmt.Errorf("%s did not answer -version (exit %d): it cannot be used for the full view", FullViewName, exit)
	}
	return bin, nil
}

// FullViewDir is where the full view should open: the configured tracker, else the current directory.
func FullViewDir(trackerDir string) string {
	if trackerDir != "" {
		return trackerDir
	}
	d, _ := os.Getwd()
	return d
}
