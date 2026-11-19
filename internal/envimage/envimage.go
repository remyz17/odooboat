// Package envimage plans environment images (ADR 0008 §11, amendment A3): it
// derives the image identity from its inputs, generates the Dockerfile, and
// writes the build context. It never talks to a runtime.
package envimage

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/remyz17/odooboat/images"
	"github.com/remyz17/odooboat/internal/runtime"
)

// Schema versions the inputs encoding. Changing the encoding changes every
// identity, which is intended: images built by older rules are not reused.
const Schema = 1

// Repository names the local tags of environment images and of local bases.
const (
	Repository     = "odooboat/environment"
	BaseRepository = "odooboat/base"
)

// Editions in the order they are presented.
const (
	Community  = "community"
	Enterprise = "enterprise"
)

var editions = []string{Community, Enterprise}

// Embedded is a source tree acquired from Git and copied into the image.
type Embedded struct {
	Git    string
	Commit string
	Dir    string // exported tree, without .git
}

// Spec is everything an environment image depends on. Mounted sources are not
// part of it: only their requirements are.
type Spec struct {
	// From is the base reference the Dockerfile builds on: a digest-pinned
	// name or a local odooboat/base tag. BaseID is its image ID.
	From           string
	BaseID         string
	Arch           string
	Series         string
	Python         string
	PostgresClient int
	// Requirements holds requirements.txt per edition that has one.
	Requirements map[string][]byte
	// Embedded holds the sources copied into the image, per edition; empty in
	// mount mode.
	Embedded map[string]Embedded
}

// SourcePin identifies an embedded source in the inputs.
type SourcePin struct {
	Git    string `json:"git"`
	Commit string `json:"commit"`
}

// Inputs is the canonical description hashed into the identity. Field order
// is fixed and map keys are sorted by encoding/json, so equal inputs always
// encode to equal bytes.
type Inputs struct {
	Schema         int                  `json:"schema"`
	Contract       int                  `json:"contract"`
	Base           string               `json:"base"`
	Arch           string               `json:"arch"`
	Series         string               `json:"series"`
	Python         string               `json:"python"`
	PostgresClient int                  `json:"postgresClient"`
	Requirements   map[string]string    `json:"requirements"`
	Declared       []string             `json:"declared"`
	Helper         string               `json:"helper"`
	Sources        map[string]SourcePin `json:"sources"`
	Recipe         string               `json:"recipe"`
}

// Plan is a ready-to-build environment image.
type Plan struct {
	Spec       Spec
	Dockerfile []byte
	Inputs     Inputs
	Identity   string // sha256:<hex>
}

// Tag is the local name the image is found by. The label is the identity;
// the tag is only an index, verified against the label before reuse.
func (p Plan) Tag() string { return Repository + ":" + strings.TrimPrefix(p.Identity, "sha256:") }

// Metadata is published as labels on the image.
func (p Plan) Metadata(base string) runtime.ImageMetadata {
	return runtime.ImageMetadata{
		Contract: runtime.ImageContract, Series: p.Spec.Series, Python: p.Spec.Python,
		Kind: runtime.ImageKindEnvironment, Inputs: p.Identity, PostgresClient: p.Spec.PostgresClient, Base: base,
	}
}

// LocalBaseTag is the tag given to a base designated by its image ID: BuildKit
// cannot build FROM a bare ID (spike docker-build-sdk).
func LocalBaseTag(id string) string {
	return BaseRepository + ":" + strings.TrimPrefix(id, "sha256:")
}

// Static context files, embedded in the binary.
const (
	helperPath      = "helper/odooboat_helper.py"
	keyPath         = "pgdg.asc"
	constraintsPath = "odoo/build-constraints.txt"
)

//go:embed dockerfile.tmpl
var dockerfileTemplate string

var dockerfile = template.Must(template.New("Dockerfile").Parse(dockerfileTemplate))

// NewPlan validates a spec, generates its Dockerfile, and computes its identity.
func NewPlan(spec Spec) (Plan, error) {
	if err := validate(spec); err != nil {
		return Plan{}, err
	}
	var requirements []string
	for _, edition := range editions {
		if _, ok := spec.Requirements[edition]; ok {
			requirements = append(requirements, edition)
		}
	}
	var embedded []string
	for _, edition := range editions {
		if _, ok := spec.Embedded[edition]; ok {
			embedded = append(embedded, edition)
		}
	}
	var out bytes.Buffer
	if err := dockerfile.Execute(&out, map[string]any{
		"From": spec.From, "Series": spec.Series, "PostgresClient": spec.PostgresClient,
		"Requirements": requirements, "Embedded": embedded,
		"PipVersion": pipVersion, "Helper": helperPath, "Key": keyPath, "Constraints": constraintsPath,
	}); err != nil {
		return Plan{}, err
	}

	recipe := sha256.New()
	recipe.Write(out.Bytes())
	for _, name := range []string{keyPath, constraintsPath} {
		data, err := fs.ReadFile(images.Environment, name)
		if err != nil {
			return Plan{}, err
		}
		fmt.Fprintf(recipe, "\x00%s\x00", name)
		recipe.Write(data)
	}
	helper, err := fs.ReadFile(images.Environment, helperPath)
	if err != nil {
		return Plan{}, err
	}

	inputs := Inputs{
		Schema: Schema, Contract: runtime.ImageContract, Base: spec.BaseID, Arch: spec.Arch,
		Series: spec.Series, Python: spec.Python, PostgresClient: spec.PostgresClient,
		Requirements: map[string]string{}, Declared: []string{}, Helper: digest(helper),
		Sources: map[string]SourcePin{}, Recipe: "sha256:" + hex.EncodeToString(recipe.Sum(nil)),
	}
	for edition, content := range spec.Requirements {
		inputs.Requirements[edition] = digest(content)
	}
	for edition, source := range spec.Embedded {
		inputs.Sources[edition] = SourcePin{Git: source.Git, Commit: source.Commit}
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Spec: spec, Dockerfile: out.Bytes(), Inputs: inputs, Identity: digest(encoded)}, nil
}

// pipVersion is the pip that builds missing wheels, as in base images.
const pipVersion = "26.3"

func validate(spec Spec) error {
	var problems []string
	if spec.From == "" || spec.BaseID == "" {
		problems = append(problems, "base reference and image ID are required")
	}
	if spec.Arch == "" || spec.Series == "" || spec.Python == "" {
		problems = append(problems, "arch, series, and python are required")
	}
	if spec.PostgresClient <= 0 {
		problems = append(problems, "postgres client major must be positive")
	}
	if _, ok := spec.Requirements[Community]; !ok {
		problems = append(problems, "community requirements are required")
	}
	for edition := range spec.Requirements {
		if !slices.Contains(editions, edition) {
			problems = append(problems, "unknown edition "+strconv.Quote(edition))
		}
	}
	for edition, source := range spec.Embedded {
		if !slices.Contains(editions, edition) {
			problems = append(problems, "unknown edition "+strconv.Quote(edition))
		}
		if source.Git == "" || source.Commit == "" || source.Dir == "" {
			problems = append(problems, edition+" embedded source needs git, commit, and an exported tree")
		}
	}
	if len(problems) > 0 {
		return errors.New("invalid environment image spec: " + strings.Join(problems, "; "))
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// epoch is the modification time of every context entry, so identical inputs
// give identical contexts and the builder's cache is reused.
var epoch = time.Unix(0, 0).UTC()

// WriteContext streams the build context as a tar archive.
func (p Plan) WriteContext(w io.Writer) error {
	tw := tar.NewWriter(w)
	file := func(name string, mode int64, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: epoch, Format: tar.FormatPAX}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := file("Dockerfile", 0o644, p.Dockerfile); err != nil {
		return err
	}
	for _, name := range []string{helperPath, keyPath, constraintsPath} {
		data, err := fs.ReadFile(images.Environment, name)
		if err != nil {
			return err
		}
		if err := file(name, 0o644, data); err != nil {
			return err
		}
	}
	for _, edition := range editions {
		if content, ok := p.Spec.Requirements[edition]; ok {
			if err := file("requirements/"+edition+".txt", 0o644, content); err != nil {
				return err
			}
		}
	}
	for _, edition := range editions {
		if source, ok := p.Spec.Embedded[edition]; ok {
			if err := addTree(tw, source.Dir, "sources/"+edition); err != nil {
				return fmt.Errorf("add %s source: %w", edition, err)
			}
		}
	}
	return tw.Close()
}

// addTree adds a directory under prefix, in lexical order, with normalized
// ownership and times. Symbolic links are kept as links.
func addTree(tw *tar.Writer, root, prefix string) error {
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		name := path.Join(prefix, filepath.ToSlash(rel))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header := &tar.Header{Name: name, ModTime: epoch, Format: tar.FormatPAX}
		switch {
		case entry.IsDir():
			header.Typeflag, header.Name, header.Mode = tar.TypeDir, name+"/", 0o755
			return tw.WriteHeader(header)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			header.Typeflag, header.Linkname, header.Mode = tar.TypeSymlink, target, 0o777
			return tw.WriteHeader(header)
		case info.Mode().IsRegular():
			header.Typeflag, header.Size, header.Mode = tar.TypeReg, info.Size(), 0o644
			if info.Mode().Perm()&0o111 != 0 {
				header.Mode = 0o755
			}
			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			f, err := os.Open(current)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		default:
			return fmt.Errorf("%s: unsupported file type %s", current, info.Mode().Type())
		}
	})
}
