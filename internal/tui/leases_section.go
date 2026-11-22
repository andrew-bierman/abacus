package tui

import (
	"context"
	"errors"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// leasesPollEvery is deliberately slow: the Leases read shells out to a shared production
// hub, so a 5s poll would be too heavy.
const leasesPollEvery = 30 * time.Second

// leasesTickMsg is the Leases refresh timer. It has its own type because the shell broadcasts
// every message to every section.
type leasesTickMsg struct{}

// leasesRead is one read of the Leases domain: the typed report, or why there is none.
type leasesRead struct {
	report       core.LeasesReport
	unavailable  string // set when there is no report
	howToConnect string // set with unavailable when the fix is something the user can do
}

type leasesMsg struct{ read leasesRead }

// readLeases reads the Leases domain through its provider.
func readLeases(ctx context.Context, p core.LeaseProvider) leasesRead {
	r, err := p.Leases(ctx)
	var nc *core.NotConnectedError
	switch {
	case errors.As(err, &nc):
		return leasesRead{unavailable: core.Sanitize(nc.Reason), howToConnect: core.Sanitize(nc.HowToConnect)}
	case err != nil:
		return leasesRead{unavailable: core.Sanitize(err.Error())}
	}
	return leasesRead{report: r}
}

// leasesSection renders the typed LeasesReport: the lease's own state and tone decide each
// badge, and no line of text is parsed.
type leasesSection struct {
	read   func(ctx context.Context) leasesRead
	got    leasesRead
	loaded bool
	tick   func() tea.Cmd // injectable so tests never sleep
	linesSection
}

func newLeasesSection(read func(context.Context) leasesRead) *leasesSection {
	return &leasesSection{read: read, linesSection: linesSection{th: DefaultTheme()}, tick: func() tea.Cmd {
		return tea.Tick(leasesPollEvery, func(time.Time) tea.Msg { return leasesTickMsg{} })
	}}
}

func (s *leasesSection) SetTheme(t Theme)        { s.th = t }
func (s *leasesSection) Capturing() bool         { return false }
func (s *leasesSection) Bindings() []key.Binding { return moveBindings() }

func (s *leasesSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return leasesMsg{read: s.read(ctx)}
	}
}

func (s *leasesSection) refresh() tea.Cmd { return s.fetch() }

func (s *leasesSection) Init() tea.Cmd { return tea.Batch(s.fetch(), s.tick()) }

func (s *leasesSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case leasesMsg:
		s.got, s.loaded = m.read, true
	case leasesTickMsg:
		return s, tea.Batch(s.fetch(), s.tick())
	case tea.WindowSizeMsg:
		s.resize(m)
	case tea.KeyMsg:
		s.scrollKey(m, len(s.lines()))
	}
	return s, nil
}

func (s *leasesSection) lines() []string {
	head := s.th.Header.Render("Leases")
	if s.loaded && s.got.unavailable != "" && s.got.howToConnect != "" {
		head += " " + s.th.Badge(KindSkip, "not connected")
	}
	return append([]string{head}, s.bodyLines()...)
}

func (s *leasesSection) bodyLines() []string {
	th := s.th
	w := s.width()
	switch {
	case !s.loaded:
		return []string{th.Text(KindMuted, "loading…")}
	case s.got.unavailable != "" && s.got.howToConnect != "":
		out := []string{""}
		out = append(out, wrap(th.Text(KindWarn, san(s.got.unavailable)), w)...)
		return append(append(out, ""), wrap(th.Text(KindAccent, "how to connect: ")+san(s.got.howToConnect), w)...)
	case s.got.unavailable != "":
		return wrap(th.Text(KindWarn, "state unavailable: "+san(s.got.unavailable)), w)
	}
	r := s.got.report
	head := th.Header.Render(san(r.Summarize()))
	if r.Source != "" && r.Source != core.SourceLive {
		head += " " + th.Badge(KindWarn, san(string(r.Source)))
	}
	out := []string{head}
	for _, l := range r.Leases {
		out = append(out, "  "+th.Badge(toneKind(l.Tone()), san(string(l.State)))+" "+san(l.Resource)+" "+th.Text(KindMuted, san(l.Detail())))
	}
	return out
}

func (s *leasesSection) View() string { return s.render(s.lines()) }
