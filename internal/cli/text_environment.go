package cli

import (
	"fmt"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/config"
)

// sourceText shows an Odoo source: a path, or a git repository at a commit.
func sourceText(p *ui.Printer, path, git, commit string) string {
	if path != "" {
		return p.Path(path)
	}
	if commit == "" {
		return git
	}
	return git + "@" + p.ID(commit)
}

// unsupportedWarnings words the dimensions outside the support matrix.
func unsupportedWarnings(dimensions []config.Dimension, series, python string, postgres int) []string {
	var out []string
	for _, d := range dimensions {
		value := python
		if d == config.DimensionPostgres {
			value = fmt.Sprint(postgres)
		}
		out = append(out, fmt.Sprintf("%s %s is outside the support matrix of %s", d, value, series))
	}
	return out
}

type buildText app.EnvironmentBuildResult

func (b buildText) RenderText(p *ui.Printer) error {
	state, pulled := "image built", "present"
	if b.Reused {
		state = "image reused"
	}
	if b.Base.Pulled {
		pulled = "pulled"
	}
	p.Headline(b.Environment, state, ui.ToneDone)
	rows := [][]string{
		{"image", p.Ref(b.Image)},
		{"id", p.ID(b.ImageID)},
		{"base", pulled, p.Ref(b.Base.Reference)},
		{"odoo", fmt.Sprintf("%s, python %s, postgres client %d", b.Series, b.Python, b.PostgresClient)},
		{"sources", b.SourceMode},
		{"community", sourceText(p, b.Community.Path, b.Community.Git, b.Community.Commit)},
	}
	if e := b.Enterprise; e != nil {
		rows = append(rows, []string{"enterprise", sourceText(p, e.Path, e.Git, e.Commit)})
	}
	p.Rows(rows...)
	p.Warnings(unsupportedWarnings(b.Unsupported, b.Series, b.Python, b.PostgresClient))
	return p.Err()
}
