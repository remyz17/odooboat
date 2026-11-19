package config

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"sync"

	"github.com/remyz17/odooboat/images"
)

// MatrixPath names the embedded matrix in messages and provenance.
const MatrixPath = "images/matrix.yaml"

// Dimension names a part of a runtime combination that an environment may
// accept outside the matrix through allowUnsupported (ADR 0008 §10). The
// vocabulary is closed; the series itself is never relaxable.
type Dimension string

const (
	DimensionPython   Dimension = "python"
	DimensionPostgres Dimension = "postgres"
)

var dimensions = []Dimension{DimensionPython, DimensionPostgres}

// --- Authored matrix document

type matrixDocument struct {
	Schema int                     `yaml:"schema"`
	Series map[string]matrixSeries `yaml:"series"`
}

type matrixSeries struct {
	Distro      string            `yaml:"distro"`
	Python      matrixPython      `yaml:"python"`
	Postgres    matrixPostgres    `yaml:"postgres"`
	Wkhtmltopdf matrixWkhtmltopdf `yaml:"wkhtmltopdf"`
}

type matrixPython struct {
	Supported []string `yaml:"supported"`
	Default   string   `yaml:"default"`
}

type matrixPostgres struct {
	Supported []int `yaml:"supported"`
	Default   int   `yaml:"default"`
}

type matrixWkhtmltopdf struct {
	Version string `yaml:"version"`
	Build   string `yaml:"build"`
}

// --- Validated matrix

// Matrix is the validated set of supported runtime combinations.
type Matrix struct {
	series map[string]SeriesEntry
	ref    docRef
}

// defaultRef locates a series default in the matrix for provenance.
func (m Matrix) defaultRef(series, leaf string) SourceRef {
	return m.ref.ref("series." + series + "." + leaf + ".default")
}

// SeriesEntry describes the supported runtime of one Odoo series.
type SeriesEntry struct {
	Series   string
	Distro   string
	Python   []string
	Postgres []int
	// DefaultPython and DefaultPostgres apply when configuration omits them.
	DefaultPython   string
	DefaultPostgres int
	Wkhtmltopdf     Wkhtmltopdf
}

// Wkhtmltopdf identifies the patched wkhtmltopdf package of a series: Version
// is the packaging release, Build the distribution it was built for.
type Wkhtmltopdf struct {
	Version string
	Build   string
}

// Lookup returns the entry for an Odoo series such as "18.0".
func (m Matrix) Lookup(series string) (SeriesEntry, bool) {
	entry, ok := m.series[series]
	return entry, ok
}

// Series lists the supported series in ascending order.
func (m Matrix) Series() []string {
	out := sortedKeys(m.series)
	slices.SortFunc(out, compareSeries)
	return out
}

// Unsupported reports which dimensions of a combination fall outside this
// series' matrix entry, in the order of the closed vocabulary. The series is
// checked by the caller through Lookup; it is never a relaxable dimension.
func (e SeriesEntry) Unsupported(python string, postgres int) []Dimension {
	// Membership is exact: one base image exists per listed Python version, so
	// a version between two listed ones is not supported.
	var out []Dimension
	if !slices.Contains(e.Python, python) {
		out = append(out, DimensionPython)
	}
	if !slices.Contains(e.Postgres, postgres) {
		out = append(out, DimensionPostgres)
	}
	return out
}

var (
	seriesRe       = regexp.MustCompile(`^[1-9][0-9]*\.0$`)
	pythonRe       = regexp.MustCompile(`^3\.(0|[1-9][0-9]*)$`)
	distroRe       = regexp.MustCompile(`^[a-z]+-[a-z0-9.]+$`)
	matrixValueRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+~-]*$`)
	embeddedMatrix = sync.OnceValues(func() (Matrix, error) { return ParseMatrix(images.Matrix) })
)

// EmbeddedMatrix returns the matrix compiled into this binary.
func EmbeddedMatrix() (Matrix, error) { return embeddedMatrix() }

// ParseMatrix strictly decodes and validates a matrix document. CI uses it too,
// so the binary and the image builds never disagree on the parsing rules.
func ParseMatrix(data []byte) (Matrix, error) {
	doc, err := decodeBytes[matrixDocument](SourceMatrix, MatrixPath, data)
	if err != nil {
		return Matrix{}, err
	}
	ref := doc.as(SourceMatrix)
	c := &collector{}
	if len(doc.doc.Series) == 0 {
		c.addField(ErrAuthored, "series", ref.ref("series"), "the matrix must define at least one series", "", "")
	}

	m := Matrix{series: map[string]SeriesEntry{}, ref: ref}
	for _, name := range sortedKeys(doc.doc.Series) {
		authored := doc.doc.Series[name]
		prefix := "series." + name
		check := func(ok bool, leaf, rule, value string) {
			if !ok {
				c.addField(ErrAuthored, FieldPath(prefix+leaf), ref.ref(prefix+leaf), rule, value, "")
			}
		}
		check(seriesRe.MatchString(name), "", `series must look like "18.0"`, name)
		check(distroRe.MatchString(authored.Distro), ".distro", `distro must look like "debian-bookworm"`, authored.Distro)
		check(matrixValueRe.MatchString(authored.Wkhtmltopdf.Version), ".wkhtmltopdf.version", "wkhtmltopdf version is required", authored.Wkhtmltopdf.Version)
		check(matrixValueRe.MatchString(authored.Wkhtmltopdf.Build), ".wkhtmltopdf.build", "wkhtmltopdf build is required", authored.Wkhtmltopdf.Build)

		python := authored.Python
		check(len(python.Supported) > 0, ".python.supported", "at least one python version is required", "")
		for i, version := range python.Supported {
			leaf := fmt.Sprintf(".python.supported[%d]", i)
			check(pythonRe.MatchString(version), leaf, `python version must look like "3.12"`, version)
			check(!slices.Contains(python.Supported[:i], version), leaf, "python version is listed twice", version)
		}
		check(slices.Contains(python.Supported, python.Default), ".python.default",
			"default python version must be one of the supported versions", python.Default)

		postgres := authored.Postgres
		check(len(postgres.Supported) > 0, ".postgres.supported", "at least one postgres major is required", "")
		for i, major := range postgres.Supported {
			leaf := fmt.Sprintf(".postgres.supported[%d]", i)
			check(major > 0, leaf, "postgres major must be positive", strconv.Itoa(major))
			check(!slices.Contains(postgres.Supported[:i], major), leaf, "postgres major is listed twice", strconv.Itoa(major))
		}
		check(slices.Contains(postgres.Supported, postgres.Default), ".postgres.default",
			"default postgres major must be one of the supported majors", strconv.Itoa(postgres.Default))

		m.series[name] = SeriesEntry{
			Series:          name,
			Distro:          authored.Distro,
			Python:          slices.Clone(python.Supported),
			Postgres:        slices.Clone(postgres.Supported),
			DefaultPython:   python.Default,
			DefaultPostgres: postgres.Default,
			Wkhtmltopdf:     Wkhtmltopdf{Version: authored.Wkhtmltopdf.Version, Build: authored.Wkhtmltopdf.Build},
		}
	}
	if err := c.err(); err != nil {
		return Matrix{}, err
	}
	return m, nil
}

// compareSeries orders "9.0" before "18.0". Series are validated beforehand.
func compareSeries(a, b string) int {
	major := func(s string) int {
		n, _ := strconv.Atoi(s[:len(s)-2])
		return n
	}
	return major(a) - major(b)
}
