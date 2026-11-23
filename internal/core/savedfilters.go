package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/query"
)

// Saved filters are named Tasks queries the user keeps. They live in abacus's own config
// directory, never in a tracker directory, and are written atomically under a lock.

const (
	// MaxSavedFilters bounds the list; a save past it is refused rather than dropping an older one.
	MaxSavedFilters = 50
	// MaxSavedFilterName is the longest name in characters.
	MaxSavedFilterName = 32
	maxFiltersFile     = 1 << 20
)

// filterNameRE: a letter or digit first, then letters, digits, space and . _ -.
var filterNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*$`)

// ErrFiltersBusy is a saved-filters file another process holds: nothing was changed.
var ErrFiltersBusy = errors.New("saved filters are busy in another abacus, so nothing was changed")

// SavedFilter is one named query.
type SavedFilter struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

// SavedFilters reads and writes the file at Path. The zero value keeps nothing (a demo session).
type SavedFilters struct{ Path string }

type filtersFile struct {
	SchemaVersion int           `json:"schema_version"`
	Filters       []SavedFilter `json:"filters"`
}

// DefaultSavedFiltersPath is $XDG_CONFIG_HOME/abacus/saved-filters.json or ~/.config/abacus/saved-filters.json.
func DefaultSavedFiltersPath() (string, error) {
	d, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", errors.New("saved filters: no home directory to keep them in")
	}
	return filepath.Join(d, "saved-filters.json"), nil
}

// DefaultSavedFilters is the store at the default path; without a usable home it keeps nothing.
func DefaultSavedFilters() SavedFilters {
	p, err := DefaultSavedFiltersPath()
	if err != nil {
		return SavedFilters{}
	}
	return SavedFilters{Path: p}
}

// ValidFilterName reports why name cannot name a saved filter ("" when it can).
func ValidFilterName(name string) string {
	switch {
	case name == "" || strings.TrimSpace(name) != name:
		return "a name has no leading or trailing space and is not empty"
	case utf8.RuneCountInString(name) > MaxSavedFilterName:
		return fmt.Sprintf("a name is at most %d characters", MaxSavedFilterName)
	case !filterNameRE.MatchString(name):
		return "a name starts with a letter or digit and holds letters, digits, space, . _ -"
	}
	return ""
}

// read is the stored list sorted by name; a missing, oversized or corrupt file reads as empty and
// corrupt says it was unusable (so a save sets the file aside instead of overwriting it).
func (f SavedFilters) read() (list []SavedFilter, corrupt bool) {
	if f.Path == "" {
		return nil, false
	}
	fi, err := os.Stat(f.Path)
	if err != nil {
		return nil, false
	}
	if fi.Size() > maxFiltersFile {
		return nil, true
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, false
	}
	var ff filtersFile
	if json.Unmarshal(b, &ff) != nil {
		return nil, true
	}
	for _, s := range ff.Filters { // never trust the file: only valid entries are shown
		if ValidFilterName(s.Name) == "" && utf8.RuneCountInString(s.Query) <= query.MaxLen && Sanitize(s.Query) == s.Query && !slices.ContainsFunc(list, func(x SavedFilter) bool { return x.Name == s.Name }) {
			list = append(list, s)
		}
	}
	if len(list) > MaxSavedFilters {
		list = list[:MaxSavedFilters]
	}
	slices.SortFunc(list, func(a, b SavedFilter) int { return strings.Compare(a.Name, b.Name) })
	return list, false
}

// List is the saved filters, sorted by name.
func (f SavedFilters) List() []SavedFilter { l, _ := f.read(); return l }

// Get is the query saved under name.
func (f SavedFilters) Get(name string) (SavedFilter, bool) {
	for _, s := range f.List() {
		if s.Name == name {
			return s, true
		}
	}
	return SavedFilter{}, false
}

// Save stores query under name, replacing a filter of that name. The query must parse.
func (f SavedFilters) Save(name, q string) error {
	if why := ValidFilterName(name); why != "" {
		return errors.New("saved filters: " + why)
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return errors.New("saved filters: there is no query to save")
	}
	if utf8.RuneCountInString(q) > query.MaxLen || Sanitize(q) != q {
		return errors.New("saved filters: the query is too long or holds control characters")
	}
	if _, err := query.Parse(q); err != nil {
		return fmt.Errorf("saved filters: the query does not parse: %w", err)
	}
	return f.update(func(list []SavedFilter) ([]SavedFilter, error) {
		for i := range list {
			if list[i].Name == name {
				list[i].Query = q
				return list, nil
			}
		}
		if len(list) >= MaxSavedFilters {
			return nil, fmt.Errorf("saved filters: at most %d are kept: delete one first", MaxSavedFilters)
		}
		return append(list, SavedFilter{Name: name, Query: q}), nil
	})
}

// Delete removes the filter saved under name.
func (f SavedFilters) Delete(name string) error {
	return f.update(func(list []SavedFilter) ([]SavedFilter, error) {
		i := slices.IndexFunc(list, func(s SavedFilter) bool { return s.Name == name })
		if i < 0 {
			return nil, errors.New("saved filters: no filter named " + Sanitize(name))
		}
		return slices.Delete(list, i, i+1), nil
	})
}

// update applies fn to the stored list under a lock and writes the result atomically (temp file,
// fsync, rename, directory fsync, mode 0600). An unusable file is kept as <name>.corrupt first.
func (f SavedFilters) update(fn func([]SavedFilter) ([]SavedFilter, error)) error {
	if f.Path == "" {
		return errors.New("saved filters: not kept in this session")
	}
	if !filepath.IsAbs(f.Path) {
		return errors.New("saved filters: the path must be absolute")
	}
	if underTrackerDir(f.Path) {
		return errors.New("saved filters: abacus does not write into a tracker directory")
	}
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("saved filters: %w", err)
	}
	unlock, err := lockWithin(f.Path, 2*time.Second)
	if errors.Is(err, errLockBusy) {
		return ErrFiltersBusy
	}
	if err != nil {
		return fmt.Errorf("saved filters: %w", err)
	}
	defer unlock()
	list, corrupt := f.read()
	if corrupt {
		kept := f.Path + ".corrupt"
		if err := os.Rename(f.Path, kept); err != nil {
			return fmt.Errorf("saved filters: could not set the unreadable file aside: %w", err)
		}
		_ = os.Chmod(kept, 0o600)
	}
	list, err = fn(list)
	if err != nil {
		return err
	}
	slices.SortFunc(list, func(a, b SavedFilter) int { return strings.Compare(a.Name, b.Name) })
	if err := writeJSONAtomic(f.Path, filtersFile{SchemaVersion: 1, Filters: list}); err != nil {
		return errors.New(strings.Replace(err.Error(), "search history:", "saved filters:", 1))
	}
	return nil
}
