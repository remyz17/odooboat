package app

import (
	"path/filepath"
	"testing"

	"github.com/remyz17/odooboat/internal/config"
)

func communityFixture(series string) string {
	return filepath.Join("..", "source", "testdata", "community-"+series)
}

func enterpriseFixture(series string) string {
	return filepath.Join("..", "source", "testdata", "enterprise-"+series)
}

func TestRuntimeReportDetectsMountedSeries(t *testing.T) {
	cfg := config.ResolvedConfig{
		OdooVersion: "18.0", Python: "3.12", Postgres: 18,
		Unsupported: []config.Dimension{config.DimensionPostgres},
		Odoo: config.ResolvedOdoo{Source: config.ResolvedOdooSource{
			Mode:       config.SourceModeMount,
			Community:  &config.ResolvedSourceLocation{Path: communityFixture("18.0")},
			Enterprise: &config.ResolvedSourceLocation{Path: enterpriseFixture("18.0")},
		}},
	}
	report := runtimeReport(cfg)
	if report.Series != "18.0" || report.Postgres != 18 || len(report.Unsupported) != 1 || !report.Source.Declared {
		t.Fatalf("report = %+v", report)
	}
	community := report.Source.Community
	if community == nil || community.DetectedSeries != "18.0" || community.Matches == nil || !*community.Matches {
		t.Fatalf("community = %+v", community)
	}
	enterprise := report.Source.Enterprise
	if enterprise == nil || enterprise.DetectedSeries != "18.0" || enterprise.Matches == nil || !*enterprise.Matches {
		t.Fatalf("enterprise = %+v", enterprise)
	}
}

func TestRuntimeReportFlagsMismatchAndUnreadableSources(t *testing.T) {
	mismatch := runtimeReport(config.ResolvedConfig{OdooVersion: "18.0", Odoo: config.ResolvedOdoo{Source: config.ResolvedOdooSource{
		Mode: config.SourceModeMount, Community: &config.ResolvedSourceLocation{Path: communityFixture("17.0")},
	}}}).Source.Community
	if mismatch.DetectedSeries != "17.0" || mismatch.Matches == nil || *mismatch.Matches {
		t.Fatalf("mismatch = %+v", mismatch)
	}

	pair := runtimeReport(config.ResolvedConfig{OdooVersion: "18.0", Odoo: config.ResolvedOdoo{Source: config.ResolvedOdooSource{
		Mode:       config.SourceModeMount,
		Community:  &config.ResolvedSourceLocation{Path: communityFixture("18.0")},
		Enterprise: &config.ResolvedSourceLocation{Path: enterpriseFixture("19.0")},
	}}}).Source
	if !*pair.Community.Matches || pair.Enterprise.DetectedSeries != "19.0" || *pair.Enterprise.Matches {
		t.Fatalf("community/enterprise pair = %+v / %+v", pair.Community, pair.Enterprise)
	}

	unreadable := runtimeReport(config.ResolvedConfig{OdooVersion: "18.0", Odoo: config.ResolvedOdoo{Source: config.ResolvedOdooSource{
		Mode: config.SourceModeMount, Community: &config.ResolvedSourceLocation{Path: t.TempDir()},
	}}}).Source.Community
	if unreadable.Matches != nil || unreadable.Note == "" {
		t.Fatalf("unreadable = %+v", unreadable)
	}

	embedded := runtimeReport(config.ResolvedConfig{OdooVersion: "18.0", Odoo: config.ResolvedOdoo{Source: config.ResolvedOdooSource{
		Mode: config.SourceModeEmbedded, Community: &config.ResolvedSourceLocation{Git: "https://example/odoo.git", Commit: "abc"},
	}}}).Source.Community
	if embedded.Matches != nil || embedded.Note != noteEmbedded {
		t.Fatalf("embedded = %+v", embedded)
	}

	if report := runtimeReport(config.ResolvedConfig{OdooVersion: "18.0"}); report.Source.Declared || report.Source.Community != nil {
		t.Fatalf("undeclared source = %+v", report.Source)
	}
}
