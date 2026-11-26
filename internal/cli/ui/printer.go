package ui

import (
	"io"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

// Tone decorates a state with a symbol and a colour. It never carries
// meaning of its own: the state always has a word (ADR 0011 §8).
type Tone int

const (
	ToneNone Tone = iota
	ToneActive
	ToneDone
	ToneIdle
	ToneWarn
	ToneFailed
)

type decoration struct {
	unicode, ascii string
	color          lipgloss.Style
	styled         bool
}

var decorations = map[Tone]decoration{
	ToneActive: {"●", "*", lipgloss.NewStyle().Foreground(lipgloss.Green), true},
	ToneDone:   {"✓", "+", lipgloss.NewStyle().Foreground(lipgloss.Green), true},
	ToneIdle:   {"○", "-", lipgloss.NewStyle(), false},
	ToneWarn:   {"⚠", "!", lipgloss.NewStyle().Foreground(lipgloss.Yellow), true},
	ToneFailed: {"✗", "x", lipgloss.NewStyle().Foreground(lipgloss.Red), true},
}

var bold = lipgloss.NewStyle().Bold(true)

// columnGap separates aligned columns.
const columnGap = "   "

// Printer writes a text result on stdout with the stream's capabilities
// (ADR 0011 §8). Styles are applied only when colour is enabled, so the
// terminal is never detected here.
type Printer struct {
	w    io.Writer
	caps Capabilities
	home string
	err  error
}

// NewPrinter returns a Printer writing to w. home is the user's home
// directory, shown as ~; empty leaves paths unchanged.
func NewPrinter(w io.Writer, caps Capabilities, home string) *Printer {
	return &Printer{w: w, caps: caps, home: strings.TrimSuffix(home, "/")}
}

// Err returns the first write error; later writes are skipped.
func (p *Printer) Err() error { return p.err }

func (p *Printer) write(s string) {
	if p.err != nil {
		return
	}
	_, p.err = io.WriteString(p.w, s)
}

// Headline writes the subject and its state: "dev  ● running".
func (p *Printer) Headline(subject, state string, tone Tone) {
	if p.caps.Color {
		subject = bold.Render(subject)
	}
	if symbol := p.symbol(tone); symbol != "" {
		state = symbol + " " + state
	}
	p.write(subject + "  " + p.Styled(state, tone) + "\n")
}

func (p *Printer) symbol(tone Tone) string {
	d, ok := decorations[tone]
	if !ok {
		return ""
	}
	if p.caps.Unicode {
		return d.unicode
	}
	return d.ascii
}

// Styled colours text with tone when colour is enabled.
func (p *Printer) Styled(text string, tone Tone) string {
	d, ok := decorations[tone]
	if !p.caps.Color || !ok || !d.styled {
		return text
	}
	return d.color.Render(text)
}

// Rows writes rows indented by two spaces, in aligned columns. A cell sets
// its column's width only when it is not the last of its row, so a long
// last value never pushes the other rows; last cells are never padded.
func (p *Printer) Rows(rows ...[]string) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row[:max(len(row)-1, 0)] {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}
	for _, row := range rows {
		var b strings.Builder
		b.WriteString("  ")
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(cell)))
				b.WriteString(columnGap)
			}
		}
		p.write(b.String() + "\n")
	}
}

// Warnings writes each message on its own line, after the result.
func (p *Printer) Warnings(messages []string) {
	for _, m := range messages {
		p.write("  " + p.Styled(p.symbol(ToneWarn)+" "+m, ToneWarn) + "\n")
	}
}

// shortID is the length identifiers and digests are cut to.
const shortID = 12

// ID drops a "sha256:" prefix and keeps the first 12 characters.
func (p *Printer) ID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > shortID {
		return id[:shortID]
	}
	return id
}

var (
	digest = regexp.MustCompile(`@sha256:([0-9a-f]{12})[0-9a-f]*`)
	// hashTag is a tag that is a full sha256, as identity tags are.
	hashTag = regexp.MustCompile(`:([0-9a-f]{12})[0-9a-f]{52}$`)
)

// Ref shortens the digest of an image reference, and a tag that is a full
// hash, to 12 characters.
func (p *Printer) Ref(ref string) string {
	return hashTag.ReplaceAllString(digest.ReplaceAllString(ref, "@sha256:$1"), ":$1")
}

// Path shows paths under the home directory with ~.
func (p *Printer) Path(path string) string {
	switch {
	case p.home == "":
		return path
	case path == p.home:
		return "~"
	case strings.HasPrefix(path, p.home+"/"):
		return "~" + path[len(p.home):]
	}
	return path
}
