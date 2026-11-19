package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The embedded matrix is part of the binary: a broken edit must fail the build.
func TestEmbeddedMatrixIsValid(t *testing.T) {
	m, err := EmbeddedMatrix()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Series()) == 0 {
		t.Fatal("embedded matrix has no series")
	}
	for _, series := range m.Series() {
		entry, _ := m.Lookup(series)
		if entry.Series != series || entry.DefaultPython == "" || entry.DefaultPostgres == 0 {
			t.Errorf("series %s: incomplete entry %+v", series, entry)
		}
	}
}

const validMatrix = `schema: 1
series:
  "18.0":
    distro: debian-bookworm
    python: {supported: ["3.10", "3.12"], default: "3.12"}
    postgres: {supported: [15, 16], default: 16}
    wkhtmltopdf: {version: "0.12.6.1-3", build: bookworm}
  "9.0":
    distro: debian-stretch
    python: {supported: ["3.5"], default: "3.5"}
    postgres: {supported: [9], default: 9}
    wkhtmltopdf: {version: "0.12.5-1", build: stretch}
`

func TestParseMatrix(t *testing.T) {
	m, err := ParseMatrix([]byte(validMatrix))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Series(); !reflect.DeepEqual(got, []string{"9.0", "18.0"}) {
		t.Fatalf("series order = %v", got)
	}
	entry, ok := m.Lookup("18.0")
	want := SeriesEntry{
		Series: "18.0", Distro: "debian-bookworm",
		Python: []string{"3.10", "3.12"}, Postgres: []int{15, 16},
		DefaultPython: "3.12", DefaultPostgres: 16,
		Wkhtmltopdf: Wkhtmltopdf{Version: "0.12.6.1-3", Build: "bookworm"},
	}
	if !ok || !reflect.DeepEqual(entry, want) {
		t.Fatalf("entry = %+v", entry)
	}
	if _, ok := m.Lookup("17.0"); ok {
		t.Fatal("17.0 must not be found")
	}
}

func TestParseMatrixRejects(t *testing.T) {
	cases := []struct {
		name, replace, with, rule string
	}{
		{"default python not supported", `default: "3.12"`, `default: "3.11"`, "default python version"},
		{"default postgres not supported", `default: 16`, `default: 14`, "default postgres major"},
		{"duplicate python", `["3.10", "3.12"]`, `["3.12", "3.12"]`, "listed twice"},
		{"bad python", `["3.10", "3.12"]`, `["3.10", "3.12.1"]`, "python version must look like"},
		{"bad series", `"18.0":`, `"saas-18.1":`, "series must look like"},
		{"missing distro", `distro: debian-bookworm`, `distro: ""`, "distro must look like"},
		{"missing wkhtmltopdf build", `build: bookworm`, `build: ""`, "wkhtmltopdf build is required"},
		{"empty postgres", `supported: [15, 16]`, `supported: []`, "at least one postgres major"},
		{"unknown field", `distro: debian-bookworm`, "distro: debian-bookworm\n    arch: amd64", "unknown field"},
		{"duplicate series", `"9.0":`, `"18.0":`, "duplicate mapping key"},
		{"newer schema", `schema: 1`, `schema: 2`, "unsupported schema version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(validMatrix, tc.replace, tc.with, 1)
			if data == validMatrix {
				t.Fatalf("replacement %q did not apply", tc.replace)
			}
			_, err := ParseMatrix([]byte(data))
			if err == nil || !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("err = %v, want rule %q", err, tc.rule)
			}
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Source.Kind != SourceMatrix || fe.Source.Path != MatrixPath {
				t.Fatalf("error must locate the matrix, got %#v", err)
			}
		})
	}
}

func TestSeriesEntryUnsupported(t *testing.T) {
	entry := SeriesEntry{Python: []string{"3.10", "3.12"}, Postgres: []int{15, 16}}
	cases := []struct {
		python   string
		postgres int
		want     []Dimension
	}{
		{"3.12", 16, nil},
		{"3.10", 15, nil},
		{"3.11", 16, []Dimension{DimensionPython}},
		{"3.12", 17, []Dimension{DimensionPostgres}},
		{"3.13", 14, []Dimension{DimensionPython, DimensionPostgres}},
	}
	for _, tc := range cases {
		got := entry.Unsupported(tc.python, tc.postgres)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Unsupported(%q, %d) = %v, want %v", tc.python, tc.postgres, got, tc.want)
		}
	}
}
