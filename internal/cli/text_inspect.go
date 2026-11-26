package cli

import (
	"fmt"
	"strings"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/workspace"
)

// Text renderings of the inspection results (ADR 0011 §8).

// refText shows a configuration source like SourceRef.String, with the
// home directory as ~.
func refText(p *ui.Printer, r config.SourceRef) string {
	var b strings.Builder
	b.WriteString(string(r.Kind))
	if r.Path != "" {
		b.WriteString(" " + p.Path(r.Path))
		if r.Line > 0 {
			fmt.Fprintf(&b, ":%d:%d", r.Line, r.Column)
		}
	}
	if r.Field != "" {
		fmt.Fprintf(&b, " (%s)", r.Field)
	}
	return b.String()
}

// connectionText shows a runtime connection: "local (docker, desktop-linux)".
func connectionText(p *ui.Printer, c workspace.Connection) string {
	where := c.Context
	if where == "" && c.Socket != "" {
		where = p.Path(c.Socket)
	}
	if where == "" {
		return fmt.Sprintf("%s (%s)", c.Alias, c.Kind)
	}
	return fmt.Sprintf("%s (%s, %s)", c.Alias, c.Kind, where)
}

type explainText app.ExplainResult

func (e explainText) RenderText(p *ui.Printer) error {
	value := e.Value
	if value == "" {
		value = "(empty)"
	}
	p.Headline(string(e.Field), value, ui.ToneNone)
	rows := [][]string{{"from", refText(p, e.Winner)}}
	for i, r := range e.Overridden {
		label := ""
		if i == 0 {
			label = "overrides"
		}
		rows = append(rows, []string{label, refText(p, r)})
	}
	if e.ListReplaced {
		rows = append(rows, []string{"list", "replaced"})
	}
	p.Rows(rows...)
	return p.Err()
}

// workspaceTone decorates a workspace status.
func workspaceTone(s app.WorkspaceStatus) ui.Tone {
	switch s {
	case app.WorkspaceStatusCurrent:
		return ui.ToneDone
	case app.WorkspaceStatusMoved, app.WorkspaceStatusDuplicate:
		return ui.ToneWarn
	}
	return ui.ToneIdle
}

type workspaceShowText app.WorkspaceShowResult

func (w workspaceShowText) RenderText(p *ui.Printer) error {
	p.Headline("workspace", string(w.Status), workspaceTone(w.Status))
	var rows [][]string
	if w.WorkspaceID != "" {
		rows = append(rows, []string{"id", p.ID(w.WorkspaceID)})
	}
	rows = append(rows,
		[]string{"root", p.Path(w.WorkspaceRoot)},
		[]string{"project", p.Path(w.ProjectFile)},
		[]string{"state", p.Path(w.StateFile)})
	if w.PreviousRoot != "" {
		rows = append(rows, []string{"previous", p.Path(w.PreviousRoot)})
	}
	if w.OriginalFile != "" {
		rows = append(rows, []string{"original", p.Path(w.OriginalFile)})
	}
	p.Rows(rows...)
	return p.Err()
}

// inspectionText shows a source and what its inspection found, in one cell.
func inspectionText(p *ui.Printer, s *app.SourceInspection) string {
	text := sourceText(p, s.Path, s.Git, s.Commit)
	switch {
	case s.Note != "":
		return text + " (" + s.Note + ")"
	case s.DetectedSeries != "" && s.Matches != nil && !*s.Matches:
		return text + " " + p.Styled("(series "+s.DetectedSeries+", differs)", ui.ToneWarn)
	case s.DetectedSeries != "":
		return text + " (series " + s.DetectedSeries + ")"
	}
	return text
}

type environmentShowText app.EnvironmentShowResult

func (e environmentShowText) RenderText(p *ui.Printer) error {
	if e.Bound {
		p.Headline(e.Environment, "bound", ui.ToneDone)
	} else {
		p.Headline(e.Environment, "not bound", ui.ToneIdle)
	}
	workspaceRow := []string{"workspace", p.Styled(string(e.WorkspaceStatus), workspaceTone(e.WorkspaceStatus))}
	if e.WorkspaceID != "" {
		workspaceRow = append(workspaceRow, p.ID(e.WorkspaceID))
	}
	rows := [][]string{workspaceRow, {"runtime", connectionText(p, e.DesiredConnection)}}
	if e.BoundConnection != nil {
		match := "matches"
		if e.Matches != nil && !*e.Matches {
			match = p.Styled("differs", ui.ToneWarn)
		}
		rows = append(rows, []string{"bound", match, connectionText(p, *e.BoundConnection)})
	}
	if engine := e.Engine; engine != nil {
		if engine.Unavailable {
			rows = append(rows, []string{"engine", "unavailable"})
		} else {
			rows = append(rows, []string{"engine", p.ID(engine.Value), engine.Source})
		}
	}
	r := e.Runtime
	rows = append(rows, []string{"odoo", fmt.Sprintf("%s, python %s, postgres %d", r.Series, r.Python, r.Postgres)})
	if r.Source.Declared {
		rows = append(rows, []string{"sources", r.Source.Mode})
	}
	if s := r.Source.Community; s != nil {
		rows = append(rows, []string{"community", inspectionText(p, s)})
	}
	if s := r.Source.Enterprise; s != nil {
		rows = append(rows, []string{"enterprise", inspectionText(p, s)})
	}
	p.Rows(rows...)
	p.Warnings(unsupportedWarnings(r.Unsupported, r.Series, r.Python, r.Postgres))
	return p.Err()
}
