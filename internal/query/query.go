// Package query is the one filter and search grammar shared by every list in abacus (the Tasks
// filter today, global search later). It is pure: Parse turns text into a typed Query or a typed
// error that carries the column of the problem, and Match evaluates a Query against any
// document through the Doc interface, so each domain maps its own fields.
//
// Grammar (the table tests in query_test.go pin every line of it):
//
//	words        every word must match; a word of up to 4 characters matches as a subsequence of
//	             a text field, a longer one as contiguous text; "quoted phrase" is contiguous
//	field:value  narrows one field; different fields AND, repeated values for one field OR
//	             (status:open status:blocked, or status:open,blocked)
//	!field:value excludes (also -field:value)
//	comparisons  priority:<=1, priority<2, updated>7d, created<2026-11-01
//	             a duration (30m 12h 7d 2w) means that long ago, so updated>7d is "touched in
//	             the last week" and updated<7d is "untouched for a week": > is later, < is earlier
//	             (on expires a duration points ahead: expires>2h is "more than 2 hours from now")
//	none         assignee:none and label:none match an empty field
//
// A field a document does not have excludes that document, so label:x only finds tasks.
package query

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxLen is the longest query text parsed.
const MaxLen = 256

// ShortWord is the longest word that matches as a subsequence rather than contiguous text.
const ShortWord = 4

// Op is a comparison operator.
type Op int

const (
	OpEq Op = iota
	OpLT
	OpLE
	OpGT
	OpGE
)

func (o Op) String() string { return [...]string{":", "<", "<=", ">", ">="}[o] }

// Word is a free-text term.
type Word struct {
	Text   string // lower-cased
	Phrase bool   // quoted: contiguous match whatever its length
}

// Pred is one field predicate.
type Pred struct {
	Field  string // canonical field name
	Neg    bool
	Op     Op
	Values []string // lower-cased; more than one only for OpEq (OR)
	Col    int      // 1-based column of the predicate in the query text
}

// Query is a parsed query. The zero Query matches everything.
type Query struct {
	Words []Word
	Preds []Pred
}

// Empty reports a query that matches every document.
func (q Query) Empty() bool { return len(q.Words) == 0 && len(q.Preds) == 0 }

// Error is a query that does not parse. Col is the 1-based column (in characters) of the problem.
type Error struct {
	Col int
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s (column %d)", e.Msg, e.Col) }

type kind int

const (
	kText   kind = iota // exact, case-insensitive
	kPrefix             // id
	kSub                // title
	kInt                // priority
	kTime               // updated, created, expires
	kBool               // blocked, ready
	kMember             // is, has, in: value must be one of a fixed set
)

type fieldDef struct {
	kind kind
	set  []string // allowed values of a kMember field
}

var domains = []string{"tasks", "board", "leases", "queues"}

// fields maps every canonical field to how it is parsed and matched.
var fields = map[string]fieldDef{
	"in":       {kMember, domains},
	"id":       {kind: kPrefix},
	"title":    {kind: kSub},
	"status":   {kind: kText},
	"who":      {kind: kText},
	"label":    {kind: kText},
	"type":     {kind: kText},
	"priority": {kind: kInt},
	"parent":   {kind: kText},
	"channel":  {kind: kText},
	"phase":    {kind: kText},
	"workflow": {kind: kText},
	"is":       {kMember, []string{"blocked", "ready", "stale"}},
	"has":      {kMember, []string{"comments", "description", "labels", "parent", "children", "assignee"}},
	"blocked":  {kind: kBool},
	"ready":    {kind: kBool},
	"updated":  {kind: kTime},
	"created":  {kind: kTime},
	"expires":  {kind: kTime},
}

var aliases = map[string]string{"assignee": "who", "author": "who", "holder": "who", "machine": "who", "at": "updated"}

func canon(name string) (string, bool) {
	name = strings.ToLower(name)
	if a, ok := aliases[name]; ok {
		name = a
	}
	_, ok := fields[name]
	return name, ok
}

// token is a run of text between spaces (spaces inside quotes do not split), with its start column.
type token struct {
	text string
	col  int
}

func tokenize(s string) ([]token, *Error) {
	var out []token
	var cur []rune
	start, col, inQ, qCol := 0, 0, false, 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, token{string(cur), start})
			cur = nil
		}
	}
	for _, r := range s {
		col++
		switch {
		case r == '"':
			if !inQ {
				qCol = col
			}
			inQ = !inQ
			if len(cur) == 0 {
				start = col
			}
			cur = append(cur, r)
		case (r == ' ' || r == '\t') && !inQ:
			flush()
		default:
			if len(cur) == 0 {
				start = col
			}
			cur = append(cur, r)
		}
	}
	if inQ {
		return nil, &Error{Col: qCol, Msg: "unterminated quote"}
	}
	flush()
	return out, nil
}

// unquote strips one pair of surrounding quotes.
func unquote(s string) (string, bool) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], true
	}
	return s, false
}

// Parse parses a query. Empty and whitespace-only text parse to the empty Query.
func Parse(s string) (Query, error) {
	if n := utf8.RuneCountInString(s); n > MaxLen {
		return Query{}, &Error{Col: MaxLen + 1, Msg: fmt.Sprintf("query is longer than %d characters", MaxLen)}
	}
	toks, err := tokenize(s)
	if err != nil {
		return Query{}, err
	}
	var q Query
	for _, t := range toks {
		if err := q.add(t); err != nil {
			return Query{}, err
		}
	}
	return q, nil
}

// splitPred splits name, operator and value of a field token: name is letters only.
func splitPred(s string) (name string, op Op, val string, ok bool, colon bool) {
	i := 0
	for i < len(s) && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
		i++
	}
	if i == 0 || i >= len(s) {
		return "", 0, "", false, false
	}
	name, rest := s[:i], s[i:]
	switch {
	case rest[0] == ':':
		rest, colon = rest[1:], true
		op = OpEq
		for _, c := range []struct {
			p string
			o Op
		}{{"<=", OpLE}, {">=", OpGE}, {"<", OpLT}, {">", OpGT}} {
			if strings.HasPrefix(rest, c.p) {
				return name, c.o, rest[len(c.p):], true, colon
			}
		}
		return name, OpEq, rest, true, colon
	case strings.HasPrefix(rest, "<="):
		return name, OpLE, rest[2:], true, false
	case strings.HasPrefix(rest, ">="):
		return name, OpGE, rest[2:], true, false
	case rest[0] == '<':
		return name, OpLT, rest[1:], true, false
	case rest[0] == '>':
		return name, OpGT, rest[1:], true, false
	}
	return "", 0, "", false, false
}

func (q *Query) add(t token) *Error {
	text, col := t.text, t.col
	neg := false
	if strings.HasPrefix(text, "!") {
		neg, text, col = true, text[1:], col+1
	} else if strings.HasPrefix(text, "-") && len(text) > 1 {
		if _, _, _, ok, _ := splitPred(text[1:]); ok {
			neg, text, col = true, text[1:], col+1
		}
	}
	name, op, val, isPred, colon := splitPred(text)
	if isPred {
		if f, known := canon(name); known {
			return q.addPred(f, neg, op, val, t.col, col+utf8.RuneCountInString(text[:len(text)-len(val)]))
		} else if colon {
			return &Error{Col: col, Msg: fmt.Sprintf("unknown field %q", strings.ToLower(name))}
		}
	}
	if neg {
		return &Error{Col: t.col, Msg: "! excludes a field value, like !status:closed"}
	}
	w, phrase := unquote(text)
	if w == "" {
		return &Error{Col: t.col, Msg: "empty quoted phrase"}
	}
	q.Words = append(q.Words, Word{Text: strings.ToLower(w), Phrase: phrase})
	return nil
}

func (q *Query) addPred(field string, neg bool, op Op, raw string, predCol, valCol int) *Error {
	def := fields[field]
	raw, quoted := unquote(raw)
	if raw == "" {
		return &Error{Col: valCol, Msg: "missing value after " + field + op.String()}
	}
	vals := []string{raw}
	if !quoted && op == OpEq {
		vals = strings.Split(raw, ",")
	}
	for i, v := range vals {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return &Error{Col: valCol, Msg: "empty value in the list for " + field}
		}
		if e := validate(field, def, op, v); e != "" {
			return &Error{Col: valCol, Msg: e}
		}
		vals[i] = v
	}
	q.Preds = append(q.Preds, Pred{Field: field, Neg: neg, Op: op, Values: vals, Col: predCol})
	return nil
}

func validate(field string, def fieldDef, op Op, v string) string {
	switch def.kind {
	case kInt:
		if _, ok := parsePriority(v); !ok {
			return fmt.Sprintf("priority must be 0-4 or p0-p4, not %q", v)
		}
	case kTime:
		if op == OpEq {
			return field + " needs a comparison, like " + field + ">7d or " + field + "<2026-11-01"
		}
		if _, ok := parseTimeSpec(v, time.Time{}); !ok {
			return fmt.Sprintf("%q is not a duration (30m 12h 7d 2w, up to 100 years) or a date (2026-11-01)", v)
		}
	case kBool:
		if op != OpEq {
			return field + " takes true or false"
		}
		if _, ok := parseBool(v); !ok {
			return fmt.Sprintf("%s takes true or false, not %q", field, v)
		}
	case kMember:
		if op != OpEq {
			return field + " takes a name, not a comparison"
		}
		for _, a := range def.set {
			if a == v {
				return ""
			}
		}
		return fmt.Sprintf("%s: %q is not one of %s", field, v, strings.Join(def.set, ", "))
	default:
		if op != OpEq {
			return field + " does not take < or >"
		}
	}
	return ""
}

func parsePriority(v string) (int, bool) {
	v = strings.TrimPrefix(v, "p")
	if len(v) == 1 && v[0] >= '0' && v[0] <= '4' {
		return int(v[0] - '0'), true
	}
	return 0, false
}

func parseBool(v string) (bool, bool) {
	switch v {
	case "true", "yes", "1":
		return true, true
	case "false", "no", "0":
		return false, true
	}
	return false, false
}

// maxSpan is the longest duration a query may name: 100 years.
const maxSpan = 100 * 365 * 24 * time.Hour

// timeSpecFor is parseTimeSpec for a field: a duration on `expires` points ahead (expires>2h is
// "expires more than two hours from now"), on every other time field it points back.
func timeSpecFor(field, v string, now time.Time) (time.Time, bool) {
	t, ok := parseTimeSpec(v, now)
	if ok && field == "expires" && len(v) >= 2 && strings.ContainsRune("mhdw", rune(v[len(v)-1])) {
		t = now.Add(now.Sub(t))
	}
	return t, ok
}

// parseTimeSpec turns a duration ("7d" is now minus 7 days), a date or an RFC 3339 time into an
// instant. With a zero now only the shape is checked.
func parseTimeSpec(v string, now time.Time) (time.Time, bool) {
	if n := len(v); n >= 2 {
		unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[v[n-1]]
		if unit > 0 {
			num := 0
			for _, c := range v[:n-1] {
				if c < '0' || c > '9' || num > 1<<20 {
					return time.Time{}, false
				}
				num = num*10 + int(c-'0')
			}
			if time.Duration(num) > maxSpan/unit { // beyond 100 years time.Duration would wrap
				return time.Time{}, false
			}
			return now.Add(-time.Duration(num) * unit), true
		}
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, strings.ToUpper(v)); err == nil {
			return t, true
		}
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// String is the normalized query: canonical field names, lower case, one space between terms.
// Parse(q.String()) is q.
func (q Query) String() string {
	var parts []string
	for _, w := range q.Words {
		if w.Phrase || strings.ContainsAny(w.Text, " \t\"") {
			parts = append(parts, `"`+w.Text+`"`)
		} else {
			parts = append(parts, w.Text)
		}
	}
	for _, p := range q.Preds {
		s := p.Field + p.Op.String()
		vals := p.Values
		if len(vals) == 1 && strings.ContainsAny(vals[0], " \t,\"") {
			s += `"` + vals[0] + `"`
		} else {
			s += strings.Join(vals, ",")
		}
		if p.Neg {
			s = "!" + s
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
