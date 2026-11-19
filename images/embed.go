// Package images holds the files that define Odooboat runtime images. Its only
// Go content is embedded files; decoding and validation belong to
// internal/config, which keeps the YAML codec behind one boundary (ADR 0003).
package images

import (
	_ "embed"
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
