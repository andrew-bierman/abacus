package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"
)

// AuditReader is an Audit whose lines can be read back (the file audit can).
type AuditReader interface {
	Entries() ([]AuditEntry, error)
}

// Entries reads the audit log; a missing file is an empty log, a line that is not an entry is skipped.
func (f *fileAudit) Entries() ([]AuditEntry, error) {
	fh, err := os.Open(f.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	defer fh.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// BoardCLINonce picks the draft nonce of a command-line board write. The idempotency key then comes from
// the content alone (target, operation, channel or message, text and this nonce) with no clock, so a dry
// run and the apply that follows it, however far apart, plan the same argv and share a plan digest.
//
// Double posts are guarded by the audit log instead of the clock: the nonce is "cli-K" for the first K
// whose key the log has never recorded, so a post made again later gets a key the board has not seen. When
// the previous key of this content was recorded within window (or is still unknown to have landed) the
// write is a repeat, and repeat is true: the caller refuses it unless the user said --again. An audit that
// cannot be read back gives K=0 and no repeat check; an unreadable log is an error (fail closed).
func BoardCLINonce(a Audit, target string, ch BoardChange, window time.Duration, now time.Time) (nonce string, repeat bool, err error) {
	r, ok := a.(AuditReader)
	if !ok {
		return "cli-0", false, nil
	}
	entries, err := r.Entries()
	if err != nil {
		return "", false, err
	}
	keyOf := func(k int) string {
		c := ch
		c.Nonce = fmt.Sprintf("cli-%d", k)
		return boardKey(target, c)
	}
	seen := func(key string) (found bool, recent bool) {
		for _, e := range entries {
			if e.Domain == string(DomainBoard) && slices.Contains(e.Argv, key) {
				found = true
				if now.Sub(e.Time) < window {
					recent = true
				}
			}
		}
		return
	}
	k := 0
	for ; ; k++ {
		if found, _ := seen(keyOf(k)); !found {
			break
		}
	}
	if k > 0 {
		_, recent := seen(keyOf(k - 1))
		repeat = recent
	}
	return fmt.Sprintf("cli-%d", k), repeat, nil
}
