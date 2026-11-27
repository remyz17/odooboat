package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/remyz17/odooboat/internal/app"
)

// tailLines is how many lines of output show below a running step.
const tailLines = 8

// minDetail is the narrowest a shortened detail may get before it is
// dropped.
const minDetail = 12

var (
	spinnerUnicode = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerASCII   = []string{"|", "/", "-", `\`}
	faint          = lipgloss.NewStyle().Faint(true)
)

// outcomeTones decorates finished steps.
var outcomeTones = map[app.Outcome]Tone{
	app.OutcomeDone: ToneDone, app.OutcomeReused: ToneDone, app.OutcomeKept: ToneDone,
	app.OutcomeSkipped: ToneIdle, app.OutcomeFailed: ToneFailed, app.OutcomeInterrupted: ToneFailed,
}

// frame is the live region at now: running steps nested under their
// visible parents, one output tail, fitted to width and height (0: no
// limit).
func (t *tty) frame(now time.Time, width, height int) []string {
	var lines []frameLine
	tailAt, tail := -1, []string(nil)
	if c := t.tailStep(); c != nil {
		tailAt, tail = c.step.seq, c.tail
	}
	for _, id := range t.roots {
		root := t.steps[id]
		if root == nil || root.finished {
			continue
		}
		t.walk(root, 0, func(s *ttyStep, depth int) {
			lines = append(lines, frameLine{text: t.stepLine(s, depth, now, width)})
			if s.seq == tailAt {
				indent := strings.Repeat("  ", depth)
				for _, l := range tail {
					lines = append(lines, frameLine{text: t.tailLine(indent, l, width), tail: true})
				}
			}
		})
	}
	return t.capHeight(lines, width, height)
}

type frameLine struct {
	text string
	tail bool
}

// walk visits s and its visible descendants in start order, depth counting
// visible ancestors; hidden steps are passed through.
func (t *tty) walk(s *ttyStep, depth int, visit func(*ttyStep, int)) {
	if !s.hidden() {
		visit(s, depth)
		depth++
	}
	for _, id := range s.children {
		if c := t.steps[id]; c != nil {
			t.walk(c, depth, visit)
		}
	}
}

// hiddenDescendants lists the hidden steps under s, through hidden steps
// only, in start order.
func (t *tty) hiddenDescendants(s *ttyStep) []*ttyStep {
	var out []*ttyStep
	for _, id := range s.children {
		c := t.steps[id]
		if c == nil || !c.hidden() {
			continue
		}
		out = append(out, c)
		out = append(out, t.hiddenDescendants(c)...)
	}
	return out
}

// current is the most recently started running hidden descendant of s.
func (t *tty) current(s *ttyStep) *ttyStep {
	var latest *ttyStep
	for _, d := range t.hiddenDescendants(s) {
		if d.running() && (latest == nil || d.seq > latest.seq) {
			latest = d
		}
	}
	return latest
}

type tailChoice struct {
	step *ttyStep
	tail []string
}

// tailStep picks the most recently started running visible step whose
// output source, the hidden step named in its detail or else itself, has
// lines.
func (t *tty) tailStep() *tailChoice {
	var best *tailChoice
	for _, s := range t.steps {
		if s.hidden() || !s.running() || !t.live(s) || (best != nil && s.seq < best.step.seq) {
			continue
		}
		source := s
		if c := t.current(s); c != nil {
			source = c
		}
		if lines := source.output.last(tailLines); len(lines) > 0 {
			best = &tailChoice{step: s, tail: lines}
		}
	}
	return best
}

// live reports whether s belongs to a top-level step still running.
func (t *tty) live(s *ttyStep) bool {
	for s.parent != nil {
		s = s.parent
	}
	return !s.finished
}

func (t *tty) stepLine(s *ttyStep, depth int, now time.Time, width int) string {
	indent := strings.Repeat("  ", depth)
	var indicator, detail string
	var took time.Duration
	if s.running() {
		frames := spinnerASCII
		if t.view.Caps.Unicode {
			frames = spinnerUnicode
		}
		indicator = frames[t.ticks%len(frames)]
		detail = t.detail(s)
		took = now.Sub(s.at)
	} else {
		indicator = t.symbol(outcomeTones[s.outcome])
		if s.outcome != app.OutcomeDone {
			detail = string(s.outcome)
		}
		took = s.end.Sub(s.at)
	}
	return fitStep(indent+indicator+" ", s.label(), sanitize(detail), elapsed(took), width, t.ellipsis())
}

func (t *tty) symbol(tone Tone) string {
	d, ok := decorations[tone]
	if !ok {
		return " "
	}
	symbol := d.ascii
	if t.view.Caps.Unicode {
		symbol = d.unicode
	}
	if t.view.Caps.Color && d.styled {
		return d.color.Render(symbol)
	}
	return symbol
}

func (t *tty) bar() string {
	if t.view.Caps.Unicode {
		return "│ "
	}
	return "| "
}

func (t *tty) ellipsis() string {
	if t.view.Caps.Unicode {
		return "…"
	}
	return "..."
}

// detail of a running step: bytes transferred by it and its hidden
// descendants, else the name of the hidden step running now.
func (t *tty) detail(s *ttyStep) string {
	var current, total int64
	seen, known := false, true
	for _, d := range append([]*ttyStep{s}, t.hiddenDescendants(s)...) {
		if d.transfer == nil {
			// A layer that finished without a transfer was already present;
			// one still running has not started downloading.
			if d != s && d.running() {
				known = false
			}
			continue
		}
		seen = true
		current += d.transfer.Current
		total += d.transfer.Total
		if d.transfer.Total <= 0 {
			known = false
		}
	}
	if seen {
		if !known {
			total = 0
		}
		return transferText(current, total)
	}
	if c := t.current(s); c != nil {
		return shortRef(c.started.Subject.Name)
	}
	return ""
}

// fitStep joins a step line's parts and fits it in width-1 cells: the
// detail goes first, then the end of the label; indicator and elapsed time
// stay unless even they do not fit.
func fitStep(prefix, label, detail, took string, width int, ellipsis string) string {
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return prefix + strings.Join(kept, "  ")
	}
	line := join(label, detail, took)
	if width <= 0 || ansi.StringWidth(line) <= width-1 {
		return line
	}
	// The detail is shortened while a useful part of it fits, then dropped.
	if detail != "" {
		room := width - 1 - ansi.StringWidth(join(label, took)) - 2
		if room >= minDetail {
			return join(label, ansi.Truncate(detail, room, ellipsis), took)
		}
	}
	line = join(label, took)
	if ansi.StringWidth(line) <= width-1 {
		return line
	}
	room := width - 1 - ansi.StringWidth(prefix) - 2 - ansi.StringWidth(took)
	if room > ansi.StringWidth(ellipsis) {
		return join(ansi.Truncate(label, room, ellipsis), took)
	}
	return ansi.Truncate(line, width-1, "")
}

// tailLine shows one output line under its step, without the output's own
// control sequences.
func (t *tty) tailLine(indent, line string, width int) string {
	text := indent + "  " + t.bar() + sanitize(line)
	if width > 0 {
		text = ansi.Truncate(text, width-1, "")
	}
	if t.view.Caps.Color {
		text = faint.Render(text)
	}
	return text
}

// sanitize drops escape sequences and control characters; a tab becomes a
// space.
func sanitize(line string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r >= 0x7f && r <= 0x9f: // C0, DEL, and C1 controls
			return -1
		}
		return r
	}, ansi.Strip(line))
}

// capHeight keeps the region within height-1 lines: tail lines go first,
// then the last steps, replaced by "… N more".
func (t *tty) capHeight(lines []frameLine, width, height int) []string {
	texts := func(ls []frameLine) []string {
		out := make([]string, len(ls))
		for i, l := range ls {
			out[i] = l.text
		}
		return out
	}
	limit := height - 1
	if height <= 0 || len(lines) <= limit {
		return texts(lines)
	}
	var steps []frameLine
	for _, l := range lines {
		if !l.tail {
			steps = append(steps, l)
		}
	}
	if len(steps) <= limit {
		return texts(steps)
	}
	if limit <= 0 {
		return nil
	}
	shown := limit - 1
	more := fmt.Sprintf("%s %d more", t.ellipsis(), len(steps)-shown)
	if width > 0 {
		more = ansi.Truncate(more, width-1, "")
	}
	return append(texts(steps[:shown]), more)
}

// units are decimal byte units.
var units = []string{"B", "kB", "MB", "GB", "TB"}

// unitOf returns the unit index and divisor for n.
func unitOf(n int64) (int, float64) {
	i, div := 0, 1.0
	for float64(n)/div >= 1000 && i < len(units)-1 {
		div *= 1000
		i++
	}
	return i, div
}

func amount(n int64, div float64) string {
	v := float64(n) / div
	if v < 10 && div > 1 {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

// transferText words bytes: "48 MB", or "48/112 MB" when the total is
// known, both in the total's unit.
func transferText(current, total int64) string {
	if total > 0 {
		i, div := unitOf(total)
		return amount(current, div) + "/" + amount(total, div) + " " + units[i]
	}
	i, div := unitOf(current)
	return amount(current, div) + " " + units[i]
}
