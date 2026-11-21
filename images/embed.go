// Package images holds the files that define Odooboat runtime images. Its only
// Go content is embedded files; decoding and validation belong to
// internal/config, which keeps the YAML codec behind one boundary (ADR 0003).
package images

import (
	"embed"
)

// Matrix is the raw content of matrix.yaml (ADR 0008 §1).
//
//go:embed matrix.yaml
var Matrix []byte

// Bases is the raw content of bases.yaml: the published base image of each
// series and Python version, pinned by digest (ADR 0008 A3).
//
//go:embed bases.yaml
var Bases []byte

// Postgres is the raw content of postgres.yaml: the PostgreSQL server image
// of each major version, pinned by digest (ADR 0009 §2).
//
//go:embed postgres.yaml
var Postgres []byte

// Environment holds the files every environment image build context
// includes: the Python helper, the PostgreSQL signing key, and the build
// constraints shared with base images (ADR 0008 A3).
//
//go:embed helper/odooboat_helper.py pgdg.asc odoo/build-constraints.txt
var Environment embed.FS
