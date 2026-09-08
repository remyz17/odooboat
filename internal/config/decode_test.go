package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestDecodeFileRejects(t *testing.T) {
	tests := []struct {
		name    string
		content string
		class   error
		want    string
	}{
		{
			name:    "unknown field",
			content: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  bogus: 1\n",
			class:   ErrDecode,
			want:    `unknown field "bogus"`,
		},
		{
			name:    "duplicate key",
			content: "schema: 1\nproject:\n  name: demo\n  name: other\n",
			class:   ErrDecode,
			want:    "duplicate mapping key",
		},
		{
			name:    "alias",
			content: "schema: 1\nx: &a demo\nproject:\n  name: *a\n",
			class:   ErrDecode,
			want:    "aliases are not supported",
		},
		{
			name:    "merge key",
			content: "schema: 1\nbase: &b {name: demo}\nproject:\n  <<: *b\n",
			class:   ErrDecode,
			want:    "merge keys are not supported",
		},
		{
			name:    "multiple documents",
			content: "schema: 1\nproject:\n  name: demo\n---\nschema: 1\n",
			class:   ErrDecode,
			want:    "more than one YAML document",
		},
		{
			name:    "explicit null",
			content: "schema: 1\nproject:\n  name: demo\n  odooVersion: null\n",
			class:   ErrDecode,
			want:    "explicit null is not supported",
		},
		{
			name:    "unsupported schema",
			content: "schema: 2\nproject:\n  name: demo\n",
			class:   ErrAuthored,
			want:    "unsupported schema version 2",
		},
		{
			name:    "missing schema",
			content: "project:\n  name: demo\n",
			class:   ErrAuthored,
			want:    `required field "schema" is missing`,
		},
		{
			name:    "empty file",
			content: "# nothing here\n",
			class:   ErrDecode,
			want:    "contains no YAML document",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, "odooboat.yaml", tc.content)
			_, err := decodeFile[ProjectDocument](SourceProject, path)
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			if !errors.Is(err, tc.class) {
				t.Errorf("error class of %v is not %v", err, tc.class)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

func TestDecodeFilePositions(t *testing.T) {
	path := writeFile(t, "odooboat.yaml", "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n")
	doc, err := decodeFile[ProjectDocument](SourceProject, path)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.doc.Project.Name != "demo" {
		t.Errorf("name = %q, want demo", doc.doc.Project.Name)
	}
	ref := doc.ref("project.odooVersion")
	if ref.Line != 4 || ref.Column != 3 {
		t.Errorf("position = %d:%d, want 4:3", ref.Line, ref.Column)
	}
	if ref.Kind != SourceProject || ref.Path != path {
		t.Errorf("ref = %+v, want kind=project path=%s", ref, path)
	}
}

func TestDecodeFileDistinguishesOmittedFromZero(t *testing.T) {
	path := writeFile(t, "odooboat.yaml",
		"schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  pythonPackages: []\n  addons:\n    web:\n      path: ./addons\n      enabled: false\n")
	doc, err := decodeFile[ProjectDocument](SourceProject, path)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.doc.Project.PythonPackages == nil {
		t.Fatal("pythonPackages should be an authored empty list, got nil")
	}
	if len(*doc.doc.Project.PythonPackages) != 0 {
		t.Errorf("pythonPackages = %v, want empty", *doc.doc.Project.PythonPackages)
	}
	addon := doc.doc.Project.Addons["web"]
	if addon.Enabled == nil || *addon.Enabled {
		t.Errorf("enabled = %v, want explicit false", addon.Enabled)
	}
}
