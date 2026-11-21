package config

import (
	"errors"
	"strings"
	"testing"
)

func TestEmbeddedPostgresImagesCoverTheMatrix(t *testing.T) {
	images, err := EmbeddedPostgresImages()
	if err != nil {
		t.Fatal(err)
	}
	matrix := mustMatrix(t)
	for _, series := range matrix.Series() {
		entry, _ := matrix.Lookup(series)
		for _, major := range entry.Postgres {
			image, ok := images.Lookup(major)
			if !ok || !strings.HasPrefix(image, "docker.io/library/postgres:") {
				t.Errorf("%s: postgres %d image = %q, %v", series, major, image, ok)
			}
		}
	}
}

func TestParsePostgresImagesRejects(t *testing.T) {
	matrix := mustMatrix(t)
	valid := map[string]string{}
	for _, major := range []string{"12", "13", "14", "15", "16", "17"} {
		valid[major] = "docker.io/library/postgres:" + major + "-bookworm" + testDigest
	}
	document := func(change func(map[string]string)) []byte {
		entries := map[string]string{}
		for k, v := range valid {
			entries[k] = v
		}
		change(entries)
		var b strings.Builder
		b.WriteString("schema: 1\nimages:\n")
		for _, k := range sortedKeys(entries) {
			b.WriteString("  \"" + k + "\": " + entries[k] + "\n")
		}
		return []byte(b.String())
	}
	if _, err := ParsePostgresImages(document(func(map[string]string) {}), matrix); err != nil {
		t.Fatalf("valid document: %v", err)
	}
	cases := map[string]func(map[string]string){
		"missing major":  func(e map[string]string) { delete(e, "16") },
		"outside matrix": func(e map[string]string) { e["11"] = "docker.io/library/postgres:11-bookworm" + testDigest },
		"not a major":    func(e map[string]string) { e["16.4"] = "docker.io/library/postgres:16-bookworm" + testDigest },
		"not pinned":     func(e map[string]string) { e["16"] = "docker.io/library/postgres:16-bookworm" },
		"wrong tag":      func(e map[string]string) { e["16"] = "docker.io/library/postgres:15-bookworm" + testDigest },
	}
	for name, change := range cases {
		if _, err := ParsePostgresImages(document(change), matrix); !errors.Is(err, ErrAuthored) {
			t.Errorf("%s: err = %v, want ErrAuthored", name, err)
		}
	}
}
