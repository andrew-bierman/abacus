package agents

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"time"
)

const (
	// maxJSONLLine bounds one record in the JSONL stores. Attachments and patches can embed
	// large payloads, and a malformed writer must not make the tab allocate without bound.
	maxJSONLLine = 10 << 20
	// maxJSONLBytesPerRead bounds how much one tick parses across every file a store reads, so
	// the first sight of long histories cannot stall the tab. The offset is the resume point,
	// so the totals are the same either way.
	maxJSONLBytesPerRead = 8 << 20
	jsonlBudgetWindow    = 200 * time.Millisecond
	// maxJSONLFiles bounds the per-path accumulations. A monitor runs for days across many
	// short sessions, so the oldest state is dropped rather than kept forever; a dropped file
	// is simply reparsed from the start if it is read again.
	maxJSONLFiles = 256
)

// jsonlCursor tracks how far one JSONL file has been read, so a tick parses only the bytes
// appended since the previous one. It is replaced wholesale when the file behind a path
// changes: a rotation, a rewrite or a truncation must not carry yesterday's totals forward.
type jsonlCursor struct {
	info   os.FileInfo
	offset int64
	// stoppedShort marks a read that spent its byte budget with bytes still unread, so the
	// drain's CaughtUp can tell "there is more to parse" from "the file ended mid-record".
	stoppedShort bool
}

// cursor lets an accumulator embedded with a jsonlCursor satisfy jsonlAccumulator.
func (c *jsonlCursor) cursor() *jsonlCursor { return c }

func (c *jsonlCursor) modTime() time.Time {
	if c.info == nil {
		return time.Time{}
	}
	return c.info.ModTime()
}

func (c *jsonlCursor) replaced(info os.FileInfo) bool {
	if c.info == nil {
		return true
	}
	return !os.SameFile(c.info, info) || info.Size() < c.offset
}

func (c *jsonlCursor) atEOF(info os.FileInfo) bool {
	return !c.replaced(info) && info.Size() <= c.offset
}

// read consumes the complete lines appended since the last read, handing each to add, and
// stops when the file is caught up or this tick's budget is spent. A trailing line without a
// newline is left unconsumed, so a record still being written is reread once and counted once.
func (c *jsonlCursor) read(f *os.File, info os.FileInfo, budget *int64, add func([]byte)) error {
	c.info = info
	c.stoppedShort = false
	if info.Size() <= c.offset {
		return nil
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		line, n, ok, err := readJSONLine(reader)
		if !ok {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		c.offset += int64(n)
		*budget -= int64(n)
		if len(line) > 0 {
			add(line)
		}
		if *budget <= 0 {
			// Only bytes past this point mean work left. A budget spent exactly on the last
			// record has caught the file up, and saying otherwise costs the drain a whole pass.
			c.stoppedShort = c.offset < info.Size()
			return nil
		}
	}
}

// readBudget is the bytes one tick may parse across every file a store reads. A caller that
// reads one transcript per live session shares it instead of paying the cap for each of them.
type readBudget struct {
	left    int64
	started time.Time
}

func (b *readBudget) take() *int64 {
	if time.Since(b.started) > jsonlBudgetWindow {
		b.left, b.started = maxJSONLBytesPerRead, time.Now()
	}
	return &b.left
}

// reset makes the next take start a fresh budget. A zero readBudget would do the same through
// the elapsed-time check; naming it keeps the three places that reset from drifting apart.
func (b *readBudget) reset() { *b = readBudget{} }

// jsonlAccumulator is one session's per-file state: the cursor it advances, the records it
// folds in, and the session it reports. Sharing it is what keeps the two JSONL stores on one
// lifecycle, so a fix to resume, rotation or trimming cannot land in only one of them.
type jsonlAccumulator interface {
	cursor() *jsonlCursor
	add([]byte)
	session() StoredSession
}

// jsonlFiles is the per-path accumulation map and shared tick budget every JSONL store keeps.
type jsonlFiles struct {
	files  map[string]jsonlAccumulator
	budget readBudget
	drain  bool
}

// BeginDrainPass switches the store to one-shot mode and starts a pass: each transcript gets the
// full byte budget (load resets it per file) and the previous pass's unfinished markers are
// cleared, so CaughtUp describes this pass alone. It is promoted to every JSONL store, so the
// join asks each store it holds.
func (s *jsonlFiles) BeginDrainPass() {
	s.drain = true
	for _, st := range s.files {
		st.cursor().stoppedShort = false
	}
}

// EndDrainPass restores the shared per-tick budget after a one-shot read, so a provider that
// also feeds a tab does not keep paying for a full history on every later read. The budget is
// reset too: a drain pass can end with it spent, and the next tab read should start a fresh one.
func (s *jsonlFiles) EndDrainPass() {
	s.drain = false
	s.budget.reset()
}

// CaughtUp reports whether the last drain pass consumed every complete record in every file the
// store read. A file left with a partial trailing line counts as caught up: that line is not a
// record yet. It is only meaningful after BeginDrainPass.
func (s *jsonlFiles) CaughtUp() bool {
	for _, st := range s.files {
		if st.cursor().stoppedShort {
			return false
		}
	}
	return true
}

// load returns the accumulated facts for the file at path, reading only the bytes appended
// since the previous call. A vanished or unreadable file returns an error; a corrupt record
// inside it is skipped.
func (s *jsonlFiles) load(path string, empty error, newAcc func() jsonlAccumulator) (StoredSession, error) {
	if path == "" {
		return StoredSession{}, empty
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			delete(s.files, path)
		}
		return StoredSession{}, err
	}
	if st, ok := s.files[path]; ok && st.cursor().atEOF(info) {
		return st.session(), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return StoredSession{}, err
	}
	defer f.Close()
	if info, err = f.Stat(); err != nil {
		return StoredSession{}, err
	}

	if s.files == nil {
		s.files = make(map[string]jsonlAccumulator)
	}
	st, ok := s.files[path]
	if !ok || st.cursor().replaced(info) {
		st = newAcc()
		s.files[path] = st
	}
	if s.drain {
		s.budget.reset()
	}
	err = st.cursor().read(f, info, s.budget.take(), st.add)
	s.trim(path)
	return st.session(), err
}

// trim drops the least recently written state once the map is full. The file just loaded is
// never the one dropped: its mtime can be the oldest, and evicting it would restart it from
// byte 0 on every read instead of letting it catch up. Re-reading a dropped file reparses it
// from the start, which is correct and rare.
func (s *jsonlFiles) trim(keep string) {
	if len(s.files) <= maxJSONLFiles {
		return
	}
	var oldestPath string
	var oldest time.Time
	for path, st := range s.files {
		if path == keep {
			continue
		}
		mod := st.cursor().modTime()
		if oldestPath == "" || mod.Before(oldest) {
			oldestPath, oldest = path, mod
		}
	}
	delete(s.files, oldestPath)
}

// readJSONLine returns one record without its line ending, plus the bytes consumed. A line
// past the cap is truncated rather than refused, so the rest of it is skipped and the next
// read resumes at a record boundary.
func readJSONLine(r *bufio.Reader) ([]byte, int, bool, error) {
	chunk, err := r.ReadSlice('\n')
	if err == nil && len(chunk) <= maxJSONLLine {
		return trimJSONLEOL(chunk), len(chunk), true, nil
	}
	var (
		line []byte
		read int
	)
	for {
		read += len(chunk)
		if room := maxJSONLLine - len(line); room > 0 {
			line = append(line, chunk[:min(room, len(chunk))]...)
		}
		if err == nil {
			return trimJSONLEOL(line), read, true, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, 0, false, err
		}
		chunk, err = r.ReadSlice('\n')
	}
}

func trimJSONLEOL(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}
