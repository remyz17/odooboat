package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/config"
)

func TestMatrixListsEveryPythonOfEverySeries(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"matrix"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	var decoded struct{ Include []build }
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	matrix, err := config.EmbeddedMatrix()
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, series := range matrix.Series() {
		entry, _ := matrix.Lookup(series)
		want += len(entry.Python)
	}
	if len(decoded.Include) != want || want == 0 {
		t.Fatalf("builds = %d, want %d", len(decoded.Include), want)
	}
	first := decoded.Include[0]
	if first.Series != "18.0" || first.Python != "3.10" || first.DistroImage != "debian:bookworm-slim" ||
		first.WkhtmltopdfVersion != "0.12.6.1-3" || first.WkhtmltopdfBuild != "bookworm" || first.Tag != "18.0-py3.10-c1" {
		t.Fatalf("first build = %+v", first)
	}
}

func TestDistroImage(t *testing.T) {
	for distro, want := range map[string]string{"debian-bookworm": "debian:bookworm-slim", "ubuntu-noble": "ubuntu:noble"} {
		if got, err := distroImage(distro); err != nil || got != want {
			t.Errorf("%s = %q, %v", distro, got, err)
		}
	}
	if _, err := distroImage("alpine-3.20"); err == nil {
		t.Error("unknown distribution family accepted")
	}
}

func inspection(labels string) string {
	return `[{"Id":"sha256:0","Config":{"Labels":` + labels + `}}]`
}

const goodLabels = `{"odooboat.image.contract":"1","odooboat.image.series":"18.0","odooboat.image.python":"3.12","odooboat.image.kind":"base","org.opencontainers.image.source":"x"}`

func TestVerify(t *testing.T) {
	verifyArgs := []string{"verify", "-series", "18.0", "-python", "3.12"}
	if err := run(verifyArgs, strings.NewReader(inspection(goodLabels)), nil); err != nil {
		t.Fatalf("good image: %v", err)
	}
	rejected := map[string]struct {
		args  []string
		input string
	}{
		"python differs":    {[]string{"verify", "-series", "18.0", "-python", "3.11"}, inspection(goodLabels)},
		"not in matrix":     {[]string{"verify", "-series", "18.0", "-python", "3.9"}, inspection(goodLabels)},
		"unknown series":    {[]string{"verify", "-series", "12.0", "-python", "3.12"}, inspection(goodLabels)},
		"environment kind":  {verifyArgs, inspection(strings.Replace(goodLabels, `"base"`, `"environment"`, 1))},
		"ownership claimed": {verifyArgs, inspection(strings.Replace(goodLabels, `}`, `,"odooboat.workspace":"w"}`, 1))},
		"no labels":         {verifyArgs, inspection(`null`)},
		"two images":        {verifyArgs, `[{"Config":{}},{"Config":{}}]`},
	}
	for name, tc := range rejected {
		if err := run(tc.args, strings.NewReader(tc.input), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRecordBase(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bases.yaml")
	if err := os.WriteFile(file, config.Bases{}.Format(), 0o644); err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/x/odooboat/odoo@sha256:" + strings.Repeat("e", 64)
	args := []string{"bases", "-file", file, "-series", "18.0", "-python", "3.12", "-image", image}
	if err := run(args, nil, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), `"3.12": `+image) {
		t.Fatalf("bases.yaml:\n%s", data)
	}
	for name, bad := range map[string][]string{
		"tag":            {"bases", "-file", file, "-series", "18.0", "-python", "3.12", "-image", "ghcr.io/x/odoo:18.0"},
		"unknown series": {"bases", "-file", file, "-series", "15.0", "-python", "3.12", "-image", image},
	} {
		if err := run(bad, nil, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	after, _ := os.ReadFile(file)
	if string(after) != string(data) {
		t.Fatal("a rejected update changed the file")
	}
}
