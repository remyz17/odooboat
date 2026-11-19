package app

import (
	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/source"
)

// RuntimeReport describes the resolved runtime combination of an environment
// and what local inspection of its Odoo sources found. It is a report: a
// mismatch is shown here and rejected by the operations that build or start.
type RuntimeReport struct {
	Series      string             `yaml:"series" json:"series"`
	Python      string             `yaml:"python" json:"python"`
	Postgres    int                `yaml:"postgres" json:"postgres"`
	Unsupported []config.Dimension `yaml:"unsupported" json:"unsupported"`
	Source      SourceReport       `yaml:"source" json:"source"`
}

type SourceReport struct {
	Declared   bool              `yaml:"declared" json:"declared"`
	Mode       string            `yaml:"mode,omitempty" json:"mode,omitempty"`
	Community  *SourceInspection `yaml:"community,omitempty" json:"community,omitempty"`
	Enterprise *SourceInspection `yaml:"enterprise,omitempty" json:"enterprise,omitempty"`
}

// SourceInspection reports the series detected in one source. Matches is set
// only when a series was detected; Note explains why it was not.
type SourceInspection struct {
	Path           string `yaml:"path,omitempty" json:"path,omitempty"`
	Git            string `yaml:"git,omitempty" json:"git,omitempty"`
	Commit         string `yaml:"commit,omitempty" json:"commit,omitempty"`
	DetectedSeries string `yaml:"detectedSeries,omitempty" json:"detectedSeries,omitempty"`
	Matches        *bool  `yaml:"matches,omitempty" json:"matches,omitempty"`
	Note           string `yaml:"note,omitempty" json:"note,omitempty"`
}

const noteEmbedded = "embedded sources are inspected when the environment image is built"

func runtimeReport(cfg config.ResolvedConfig) RuntimeReport {
	src := cfg.Odoo.Source
	report := RuntimeReport{
		Series: cfg.OdooVersion, Python: cfg.Python, Postgres: cfg.Postgres, Unsupported: cfg.Unsupported,
		Source: SourceReport{Declared: src.Declared(), Mode: src.Mode},
	}
	if src.Community != nil {
		report.Source.Community = inspect(*src.Community, cfg.OdooVersion, source.ReadSeries)
	}
	if src.Enterprise != nil {
		report.Source.Enterprise = inspect(*src.Enterprise, cfg.OdooVersion, source.ReadEnterpriseSeries)
	}
	return report
}

// inspect compares the series read from a mounted tree with the configured
// series. Both editions are compared with odooVersion, so a community and
// enterprise pair of different series always shows a mismatch.
func inspect(location config.ResolvedSourceLocation, expected string, read func(string) (string, error)) *SourceInspection {
	inspection := located(location)
	if location.Mode() == config.SourceModeEmbedded {
		inspection.Note = noteEmbedded
		return &inspection
	}
	series, err := read(location.Path)
	if err != nil {
		inspection.Note = err.Error()
		return &inspection
	}
	matches := series == expected
	inspection.DetectedSeries, inspection.Matches = series, &matches
	return &inspection
}

func located(location config.ResolvedSourceLocation) SourceInspection {
	return SourceInspection{Path: location.Path, Git: location.Git, Commit: location.Commit}
}
