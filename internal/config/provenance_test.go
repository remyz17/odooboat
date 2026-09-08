package config

import (
	"path/filepath"
	"testing"
)

func TestProvenanceRecordsWinnersAndOverrides(t *testing.T) {
	root := fixtureRoot(t)
	projectFile := filepath.Join(root, "project", ProjectFileName)
	localFile := filepath.Join(root, "project", LocalFileName)
	userFile := filepath.Join(root, "user.yaml")

	version := "16.0"
	result := resolveFixture(t, "dev", InvocationPatch{OdooVersion: &version})
	prov := result.Provenance

	tests := []struct {
		name         string
		field        FieldPath
		winnerKind   SourceKind
		winnerPath   string
		overridden   []SourceKind
		listReplaced bool
		secret       bool
	}{
		{
			name:       "invocation override",
			field:      FieldOdooVersion,
			winnerKind: SourceInvocation,
			overridden: []SourceKind{SourceProject, SourceEnvironment},
		},
		{
			name:       "local environment override",
			field:      FieldRuntimeConnection,
			winnerKind: SourceLocal,
			winnerPath: localFile,
			overridden: []SourceKind{SourceProject},
		},
		{
			name:         "list replaced by the environment overlay",
			field:        FieldPythonPackages,
			winnerKind:   SourceEnvironment,
			winnerPath:   projectFile,
			overridden:   []SourceKind{SourceDefault, SourceProject},
			listReplaced: true,
		},
		{
			name:       "keyed entry overridden by the local document",
			field:      AddonField("core", "path"),
			winnerKind: SourceLocal,
			winnerPath: localFile,
			overridden: []SourceKind{SourceProject},
		},
		{
			name:       "inherited keyed entry keeps its project origin",
			field:      AddonField("extra", "path"),
			winnerKind: SourceProject,
			winnerPath: projectFile,
		},
		{
			name:       "default is recorded when nothing authors the field",
			field:      AddonField("core", "enabled"),
			winnerKind: SourceDefault,
		},
		{
			name:       "user preference",
			field:      FieldPreferencesColor,
			winnerKind: SourceUser,
			winnerPath: userFile,
			overridden: []SourceKind{SourceDefault},
		},
		{
			name:       "secret carries a source but no value",
			field:      FieldOdooMasterPassword,
			winnerKind: SourceProject,
			winnerPath: projectFile,
			secret:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := prov.Lookup(tc.field)
			if !ok {
				t.Fatalf("no provenance recorded for %s", tc.field)
			}
			if entry.Winner.Kind != tc.winnerKind {
				t.Errorf("winner kind = %q, want %q", entry.Winner.Kind, tc.winnerKind)
			}
			if tc.winnerPath != "" && entry.Winner.Path != tc.winnerPath {
				t.Errorf("winner path = %q, want %q", entry.Winner.Path, tc.winnerPath)
			}
			if tc.winnerPath != "" && entry.Winner.Line == 0 {
				t.Error("winner should carry a source line")
			}
			if len(entry.Overridden) != len(tc.overridden) {
				t.Fatalf("overridden = %+v, want %v", entry.Overridden, tc.overridden)
			}
			for i, kind := range tc.overridden {
				if entry.Overridden[i].Kind != kind {
					t.Errorf("overridden[%d] = %q, want %q", i, entry.Overridden[i].Kind, kind)
				}
			}
			if entry.ListReplaced != tc.listReplaced {
				t.Errorf("listReplaced = %v, want %v", entry.ListReplaced, tc.listReplaced)
			}
			if entry.Secret != tc.secret {
				t.Errorf("secret = %v, want %v", entry.Secret, tc.secret)
			}
		})
	}
}

func TestProvenanceFieldsAreSorted(t *testing.T) {
	fields := resolveFixture(t, "dev", InvocationPatch{}).Provenance.Fields()
	for i := 1; i < len(fields); i++ {
		if fields[i-1] >= fields[i] {
			t.Fatalf("fields are not sorted: %q before %q", fields[i-1], fields[i])
		}
	}
}
