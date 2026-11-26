package cli

import (
	"cmp"
	"slices"
	"strings"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/runtime"
)

// Text renderings of the lifecycle results (ADR 0011 §8). Each is a CLI
// type over the application result, which stays free of presentation.

// resourceOrder lists containers first (Odoo, then PostgreSQL, then other
// roles by name), then the network, then the volumes.
var resourceOrder = map[runtime.Role]int{
	runtime.RoleOdoo: 0, runtime.RolePostgres: 1, runtime.RoleEnvironment: 3,
	runtime.RolePostgresData: 4, runtime.RoleOdooData: 5,
}

func sortResources(reports []app.ResourceReport) []app.ResourceReport {
	rank := func(r app.ResourceReport) int {
		if n, ok := resourceOrder[r.Role]; ok {
			return n
		}
		return 2
	}
	sorted := slices.Clone(reports)
	slices.SortStableFunc(sorted, func(a, b app.ResourceReport) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a.Role, b.Role), cmp.Compare(a.Name, b.Name))
	})
	return sorted
}

func ofKind(reports []app.ResourceReport, kind runtime.ResourceType) []app.ResourceReport {
	var out []app.ResourceReport
	for _, r := range sortResources(reports) {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

const stateRunning = "running"

type statusText app.StatusResult

func (s statusText) RenderText(p *ui.Printer) error {
	containers := ofKind(s.Resources, runtime.ResourceContainer)
	state, tone := s.state(containers)
	p.Headline(s.Environment, state, tone)

	var rows [][]string
	// The URL answers only when the application says the environment runs.
	if s.Running && s.URL != "" {
		rows = append(rows, []string{"url", s.URL})
	}
	for _, c := range containers {
		stateTone := ui.ToneWarn
		if c.State == stateRunning {
			stateTone = ui.ToneActive
		}
		rows = append(rows, []string{string(c.Role), p.Styled(c.State, stateTone), c.Name})
	}
	var volumes []string
	for _, v := range ofKind(s.Resources, runtime.ResourceVolume) {
		volumes = append(volumes, v.Name)
	}
	if len(volumes) > 0 {
		rows = append(rows, []string{"volumes", strings.Join(volumes, ", ")})
	}
	for _, o := range sortResources(s.Orphans) {
		rows = append(rows, []string{"orphan", string(o.Kind), o.Name})
	}
	p.Rows(rows...)
	p.Warnings(s.Warnings)
	return p.Err()
}

// state is the headline state: running, partial, stopped, down, or not bound.
// running is the application's verdict, never recomputed here.
func (s statusText) state(containers []app.ResourceReport) (string, ui.Tone) {
	running := 0
	for _, c := range containers {
		if c.State == stateRunning {
			running++
		}
	}
	switch {
	case !s.Bound:
		return "not bound", ui.ToneIdle
	case len(containers) == 0:
		return "down", ui.ToneIdle
	case s.Running:
		return stateRunning, ui.ToneActive
	case running > 0:
		return "partial", ui.ToneWarn
	}
	return "stopped", ui.ToneIdle
}

// resourceLabel names a resource by its role; the network by its kind.
func resourceLabel(r app.ResourceReport) string {
	if r.Kind == runtime.ResourceNetwork {
		return string(runtime.ResourceNetwork)
	}
	return string(r.Role)
}

// resourceRows gives one "<label>  <word>  <name>" row per resource, in
// resource order, with the reason as a fourth cell when there is one.
func resourceRows(p *ui.Printer, reports []app.ResourceReport, word func(app.ResourceReport) (string, ui.Tone)) [][]string {
	var rows [][]string
	for _, r := range sortResources(reports) {
		text, tone := word(r)
		row := []string{resourceLabel(r), p.Styled(text, tone), r.Name}
		if r.Reason != "" {
			row = append(row, r.Reason)
		}
		rows = append(rows, row)
	}
	return rows
}

// actionWord styles what up did: a change is Done, kept is plain.
func actionWord(r app.ResourceReport) (string, ui.Tone) {
	if r.Action == app.ActionKept {
		return string(r.Action), ui.ToneNone
	}
	return string(r.Action), ui.ToneDone
}

type upText app.UpResult

func (u upText) RenderText(p *ui.Printer) error {
	p.Headline(u.Environment, stateRunning, ui.ToneActive)
	built, pulled := "built", "present"
	if u.Odoo.Reused {
		built = "reused"
	}
	if u.Postgres.Pulled {
		pulled = "pulled"
	}
	rows := [][]string{
		{"url", u.URL},
		{"odoo image", built, p.Ref(u.Odoo.Image)},
		{"postgres image", pulled, p.Ref(u.Postgres.Image)},
	}
	p.Rows(append(rows, resourceRows(p, u.Resources, actionWord)...)...)
	p.Warnings(u.Warnings)
	return p.Err()
}

type downText app.DownResult

func (d downText) RenderText(p *ui.Printer) error {
	p.Headline(d.Environment, "down", ui.ToneIdle)
	word := func(text string, tone ui.Tone) func(app.ResourceReport) (string, ui.Tone) {
		return func(app.ResourceReport) (string, ui.Tone) { return text, tone }
	}
	rows := resourceRows(p, d.Removed, word(string(app.ActionRemoved), ui.ToneDone))
	rows = append(rows, resourceRows(p, d.Kept, word(string(app.ActionKept), ui.ToneNone))...)
	rows = append(rows, resourceRows(p, d.Skipped, word("skipped", ui.ToneWarn))...)
	p.Rows(rows...)
	return p.Err()
}
