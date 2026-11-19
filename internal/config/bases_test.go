package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/images"
)

var testDigest = "@sha256:" + strings.Repeat("c", 64)

// The committed file is valid and canonical: CI rewrites it with Format, so a
// hand edit would show up as an unrelated diff.
func TestEmbeddedBasesAreValid(t *testing.T) {
	bases, err := EmbeddedBases()
	if err != nil {
		t.Fatal(err)
	}
	if string(bases.Format()) != string(images.Bases) {
		t.Fatalf("%s is not in canonical form:\n%s", BasesPath, bases.Format())
	}
}

func TestBasesFormatRoundTrip(t *testing.T) {
	matrix := mustMatrix(t)
	empty, err := ParseBases(Bases{}.Format(), matrix)
	if err != nil {
		t.Fatalf("empty document: %v", err)
	}
	b := empty.With("18.0", "3.12", "ghcr.io/x/odoo"+testDigest).With("18.0", "3.10", "ghcr.io/x/odoo:a"+testDigest)
	formatted := b.Format()
	if !strings.Contains(string(formatted), `"3.10": ghcr.io/x/odoo:a`) ||
		strings.Index(string(formatted), `"3.10"`) > strings.Index(string(formatted), `"3.12"`) {
		t.Fatalf("formatted:\n%s", formatted)
	}
	parsed, err := ParseBases(formatted, matrix)
	if err != nil {
		t.Fatalf("parse formatted: %v\n%s", err, formatted)
	}
	image, ref, ok := parsed.Lookup("18.0", "3.12")
	if !ok || image != "ghcr.io/x/odoo"+testDigest || ref.Kind != SourceBases || ref.Path != BasesPath || ref.Line == 0 {
		t.Fatalf("lookup = %q %+v %v", image, ref, ok)
	}
	if string(parsed.Format()) != string(formatted) {
		t.Fatal("formatting is not stable")
	}
	if _, _, ok := parsed.Lookup("18.0", "3.11"); ok {
		t.Fatal("unexpected entry for 3.11")
	}
	// With never mutates its receiver.
	if _, _, ok := empty.Lookup("18.0", "3.12"); ok {
		t.Fatal("With mutated the original")
	}
}

func TestParseBasesRejects(t *testing.T) {
	cases := map[string]string{
		"unknown series":     "schema: 1\nbases:\n  \"15.0\":\n    \"3.12\": ghcr.io/x/odoo" + testDigest + "\n",
		"unsupported python": "schema: 1\nbases:\n  \"18.0\":\n    \"3.9\": ghcr.io/x/odoo" + testDigest + "\n",
		"tag only":           "schema: 1\nbases:\n  \"18.0\":\n    \"3.12\": ghcr.io/x/odoo:18.0\n",
		"local image ID":     "schema: 1\nbases:\n  \"18.0\":\n    \"3.12\": sha256:" + strings.Repeat("c", 64) + "\n",
		"future schema":      "schema: 2\nbases: {}\n",
		"unknown field":      "schema: 1\nbases: {}\nextra: 1\n",
	}
	for name, doc := range cases {
		_, err := ParseBases([]byte(doc), mustMatrix(t))
		if !errors.Is(err, ErrAuthored) && !errors.Is(err, ErrDecode) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), BasesPath) {
			t.Errorf("%s: error does not locate %s: %v", name, BasesPath, err)
		}
	}
}

// A published base is the default base image of its series and Python
// version, with provenance pointing at bases.yaml; configuration overrides it.
func TestPublishedBaseIsTheDefault(t *testing.T) {
	published := "ghcr.io/x/odoo" + testDigest
	saved := embeddedBases
	t.Cleanup(func() { embeddedBases = saved })
	bases, err := ParseBases(Bases{}.With("18.0", "3.12", published).Format(), mustMatrix(t))
	if err != nil {
		t.Fatal(err)
	}
	embeddedBases = func() (Bases, error) { return bases, nil }

	result, err := resolveDocuments(t, projectWith("", ""), validUserDocument)
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.BaseImage != published {
		t.Fatalf("base image = %q", result.Config.BaseImage)
	}
	if ref := result.Provenance[FieldBaseImage].Winner; ref.Kind != SourceBases || ref.Path != BasesPath {
		t.Fatalf("provenance = %+v", ref)
	}

	// Another Python version has no published base.
	result, err = resolveDocuments(t, projectWith("  python: \"3.11\"\n", ""), validUserDocument)
	if err != nil || result.Config.BaseImage != "" {
		t.Fatalf("python 3.11: base = %q, %v", result.Config.BaseImage, err)
	}

	local := "sha256:" + strings.Repeat("d", 64)
	result, err = resolveDocuments(t, projectWith("  baseImage: "+local+"\n", ""), validUserDocument)
	if err != nil || result.Config.BaseImage != local || result.Provenance[FieldBaseImage].Winner.Kind != SourceProject {
		t.Fatalf("override: base = %q, prov %+v, %v", result.Config.BaseImage, result.Provenance[FieldBaseImage].Winner, err)
	}
}
