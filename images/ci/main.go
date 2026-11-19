// Command ci drives the base image workflow from the embedded matrix, so the
// published images and the odooboat binary read images/matrix.yaml with the
// same parser (ADR 0008 A1.1).
//
//	go run ./images/ci matrix
//	    Print the GitHub Actions build matrix as JSON.
//	docker image inspect REF | go run ./images/ci verify -series 18.0 -python 3.12
//	    Check the metadata of a built base image against the matrix.
//	go run ./images/ci bases -series 18.0 -python 3.12 -image REF@sha256:…
//	    Record a published base image in images/bases.yaml.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/runtime"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ci:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ci matrix | ci verify -series SERIES -python PYTHON | ci bases -series SERIES -python PYTHON -image REF")
	}
	matrix, err := config.EmbeddedMatrix()
	if err != nil {
		return err
	}
	switch args[0] {
	case "matrix":
		builds, err := buildMatrix(matrix)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string][]build{"include": builds})
	case "verify":
		flags := flag.NewFlagSet("verify", flag.ContinueOnError)
		series := flags.String("series", "", "expected Odoo series")
		python := flags.String("python", "", "expected Python version")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return verify(matrix, *series, *python, stdin)
	case "bases":
		flags := flag.NewFlagSet("bases", flag.ContinueOnError)
		file := flags.String("file", config.BasesPath, "bases document to update")
		series := flags.String("series", "", "Odoo series")
		python := flags.String("python", "", "Python version")
		image := flags.String("image", "", "published image pinned by digest")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return recordBase(matrix, *file, *series, *python, *image)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// build is one base image: a series and one of its Python versions. Field
// names are the matrix variables used by .github/workflows/images.yml.
type build struct {
	Series             string `json:"series"`
	Python             string `json:"python"`
	DistroImage        string `json:"distroImage"`
	WkhtmltopdfVersion string `json:"wkhtmltopdfVersion"`
	WkhtmltopdfBuild   string `json:"wkhtmltopdfBuild"`
	// Tag is the stable part of the published tags; the workflow appends the
	// build date and run number (ADR 0008 §13).
	Tag string `json:"tag"`
}

func buildMatrix(matrix config.Matrix) ([]build, error) {
	var builds []build
	for _, series := range matrix.Series() {
		entry, _ := matrix.Lookup(series)
		image, err := distroImage(entry.Distro)
		if err != nil {
			return nil, fmt.Errorf("series %s: %w", series, err)
		}
		for _, python := range entry.Python {
			builds = append(builds, build{
				Series: series, Python: python, DistroImage: image,
				WkhtmltopdfVersion: entry.Wkhtmltopdf.Version, WkhtmltopdfBuild: entry.Wkhtmltopdf.Build,
				Tag: fmt.Sprintf("%s-py%s-c%d", series, python, runtime.ImageContract),
			})
		}
	}
	return builds, nil
}

// distroImage maps a matrix distribution to the image the base is built from.
// The list is closed: a new distribution family is a deliberate decision.
func distroImage(distro string) (string, error) {
	family, codename, ok := strings.Cut(distro, "-")
	switch {
	case ok && family == "debian":
		return "debian:" + codename + "-slim", nil
	case ok && family == "ubuntu":
		return "ubuntu:" + codename, nil
	}
	return "", fmt.Errorf("distribution %q has no base image mapping", distro)
}

// verify reads `docker image inspect` output and checks the image metadata
// against the requested matrix combination.
func verify(matrix config.Matrix, series, python string, stdin io.Reader) error {
	entry, ok := matrix.Lookup(series)
	if !ok {
		return fmt.Errorf("series %q is not in the matrix", series)
	}
	if !slices.Contains(entry.Python, python) {
		return fmt.Errorf("python %q is not supported for series %s", python, series)
	}
	var inspected []struct {
		Config struct {
			Labels map[string]string
		}
	}
	if err := json.NewDecoder(stdin).Decode(&inspected); err != nil {
		return fmt.Errorf("decode image inspection: %w", err)
	}
	if len(inspected) != 1 {
		return fmt.Errorf("expected one inspected image, got %d", len(inspected))
	}
	metadata, err := runtime.DecodeImageMetadata(inspected[0].Config.Labels)
	if err != nil {
		return err
	}
	want := runtime.ImageMetadata{Contract: runtime.ImageContract, Series: series, Python: python, Kind: runtime.ImageKindBase}
	if metadata != want {
		return fmt.Errorf("image metadata %+v, want %+v", metadata, want)
	}
	return nil
}

// recordBase rewrites the bases document with one entry set. The result is
// parsed again before it is written, so CI never commits an invalid file.
func recordBase(matrix config.Matrix, file, series, python, image string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	bases, err := config.ParseBases(data, matrix)
	if err != nil {
		return err
	}
	updated := bases.With(series, python, image).Format()
	if _, err := config.ParseBases(updated, matrix); err != nil {
		return err
	}
	return os.WriteFile(file, updated, 0o644)
}
