package bd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// History reads one task's event log (`bd --readonly history ID --events --json --limit=N`), newest
// first, at most limit events (the report says when there were more). It is a read of the tracker's
// own history; a comment is not an event in bd 1.3.4, so a comment alone does not appear.
func (m *Module) History(ctx context.Context, id string, limit int) (core.TaskHistoryReport, error) {
	if !core.TaskIDRE.MatchString(id) {
		return core.TaskHistoryReport{}, fmt.Errorf("invalid issue id %q", id)
	}
	if limit < 1 || limit > core.HistoryMaxLimit {
		limit = core.HistoryDefaultLimit
	}
	// One more than asked for, so "there is more" is known without a second read.
	out, exit, err := m.read(ctx, []string{"--readonly", "history", id, "--events", "--json", fmt.Sprintf("--limit=%d", limit+1)})
	switch {
	case errors.Is(err, core.ErrTimedOut):
		return core.TaskHistoryReport{}, fmt.Errorf("bd history timed out: %w", core.ErrTimedOut)
	case err != nil:
		return core.TaskHistoryReport{}, fmt.Errorf("bd history could not run: %s", core.Sanitize(err.Error()))
	case exit != 0:
		return core.TaskHistoryReport{}, fmt.Errorf("bd history exited %d", exit)
	}
	return taskview.ParseHistory(out, id, limit, time.Now())
}

var dbNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Fingerprint is a cheap change check that runs no process: the content of the embedded Dolt store's
// manifest, a file of about 150 bytes that bd rewrites on every write and never on a read (measured
// against bd 1.3.4, see testdata/README.md). Two equal fingerprints mean nothing was written between.
//
// core.ErrNoFingerprint says the tracker has no usable manifest (not an embedded store, no such file,
// or something that is not a small regular file: a symlink, a FIFO, a directory). core.ErrFingerprintFailed
// says only this read failed (I/O error, timeout). The read runs in its own goroutine and gives up when
// ctx does, so a hung filesystem cannot hold the caller past its deadline. The files are found through
// the tracker directory abacus was pointed at: a bd redirect or an environment override could make bd
// use a different store, which this does not see.
func (m *Module) Fingerprint(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %v", core.ErrFingerprintFailed, err)
	}
	type result struct {
		fp  string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		fp, err := m.fingerprint()
		ch <- result{fp, err}
	}()
	select {
	case r := <-ch:
		return r.fp, r.err
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %v", core.ErrFingerprintFailed, ctx.Err())
	}
}

func (m *Module) fingerprint() (string, error) {
	root := core.TrackerRoot(m.TrackerDir())
	if root == "" {
		return "", core.ErrNoFingerprint
	}
	b, err := readSmall(filepath.Join(root, ".beads", "metadata.json"), 64<<10)
	if err != nil {
		return "", err
	}
	var meta struct {
		Mode string `json:"dolt_mode"`
		DB   string `json:"dolt_database"`
	}
	if json.Unmarshal(b, &meta) != nil || meta.Mode != "embedded" || !dbNameRE.MatchString(meta.DB) {
		return "", core.ErrNoFingerprint
	}
	man, err := readSmall(filepath.Join(root, ".beads", "embeddeddolt", meta.DB, ".dolt", "noms", "manifest"), 8<<10)
	if err != nil {
		return "", err
	}
	if len(man) == 0 {
		return "", core.ErrNoFingerprint
	}
	h := sha256.Sum256(man)
	return hex.EncodeToString(h[:8]), nil
}

// readSmall reads a regular file of at most max bytes and nothing else. The type is checked before
// the file is opened (a FIFO would block the open for ever), the file is opened without following a
// symlink and without blocking, and the type and size are checked again on the open descriptor.
func readSmall(path string, max int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, core.ErrNoFingerprint
	case err != nil:
		return nil, fmt.Errorf("%w: %v", core.ErrFingerprintFailed, err)
	case !fi.Mode().IsRegular() || fi.Size() > max:
		return nil, core.ErrNoFingerprint
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNoBlockNoFollow, 0)
	switch {
	case errors.Is(err, os.ErrNotExist) || isSymlinkRefusal(err):
		return nil, core.ErrNoFingerprint
	case err != nil:
		return nil, fmt.Errorf("%w: %v", core.ErrFingerprintFailed, err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, fmt.Errorf("%w: %v", core.ErrFingerprintFailed, err)
	} else if !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, core.ErrNoFingerprint
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", core.ErrFingerprintFailed, err)
	}
	return b, nil
}

// SetTrackerDir re-points every module that shares the tracker reference at dir. Core has already
// validated and confirmed the switch; this only refuses a module that was built without a shared
// reference, and a directory that is not a tracker.
func (m *Module) SetTrackerDir(dir string) error {
	if m.o.Ref == nil {
		return errors.New("this session cannot switch tracker")
	}
	if _, err := core.ValidateTrackerDir(dir); err != nil {
		return err
	}
	m.o.Ref.Set(dir)
	return nil
}
