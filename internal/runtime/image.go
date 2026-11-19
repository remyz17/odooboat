package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// Image metadata keys (ADR 0008 §5). They describe an image and are read from
// image inspection; containers inherit them but ownership ignores them.
const (
	LabelImageContract = ImageMetadataPrefix + "contract"
	LabelImageSeries   = ImageMetadataPrefix + "series"
	LabelImagePython   = ImageMetadataPrefix + "python"
	LabelImageKind     = ImageMetadataPrefix + "kind"
	// Environment images only (ADR 0008 A3).
	LabelImageInputs         = ImageMetadataPrefix + "inputs"
	LabelImagePostgresClient = ImageMetadataPrefix + "postgres-client"
	LabelImageBase           = ImageMetadataPrefix + "base"
)

// ImageContract is the version of the image contract this binary implements:
// fixed paths, execution user, and entrypoint arguments (ADR 0008 A2).
const ImageContract = 1

type ImageKind string

const (
	ImageKindBase        ImageKind = "base"
	ImageKindEnvironment ImageKind = "environment"
)

// ErrInvalidImage reports image metadata that is missing, malformed, or
// claims ownership.
var ErrInvalidImage = errors.New("invalid image metadata")

// ImageMetadata is the published description of an Odooboat image. Inputs,
// PostgresClient, and Base are set on environment images only.
type ImageMetadata struct {
	Contract       int
	Series         string
	Python         string
	Kind           ImageKind
	Inputs         string
	PostgresClient int
	Base           string
}

var inputsRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Labels returns the labels that publish the metadata.
func (m ImageMetadata) Labels() map[string]string {
	labels := map[string]string{
		LabelImageContract: strconv.Itoa(m.Contract),
		LabelImageSeries:   m.Series,
		LabelImagePython:   m.Python,
		LabelImageKind:     string(m.Kind),
	}
	if m.Kind == ImageKindEnvironment {
		labels[LabelImageInputs] = m.Inputs
		labels[LabelImagePostgresClient] = strconv.Itoa(m.PostgresClient)
		labels[LabelImageBase] = m.Base
	}
	return labels
}

// DecodeImageMetadata reads the metadata of an Odooboat image. It rejects
// images that claim ownership, lack a required key, or declare a contract
// this binary does not implement. Unknown odooboat.image.* keys are ignored so
// later binaries can add metadata without breaking earlier ones.
func DecodeImageMetadata(labels map[string]string) (ImageMetadata, error) {
	if err := CheckImageLabels(labels); err != nil {
		return ImageMetadata{}, fmt.Errorf("%w: %v", ErrInvalidImage, err)
	}
	if err := requireLabels(labels, LabelImageContract, LabelImageSeries, LabelImagePython, LabelImageKind); err != nil {
		return ImageMetadata{}, err
	}
	contract, err := canonicalInt(labels, LabelImageContract)
	if err != nil {
		return ImageMetadata{}, err
	}
	if contract != ImageContract {
		return ImageMetadata{}, fmt.Errorf("%w: image contract %d is not supported by this binary (supported: %d)",
			ErrInvalidImage, contract, ImageContract)
	}
	m := ImageMetadata{Contract: contract, Series: labels[LabelImageSeries], Python: labels[LabelImagePython],
		Kind: ImageKind(labels[LabelImageKind])}
	switch m.Kind {
	case ImageKindBase:
		return m, nil
	case ImageKindEnvironment:
	default:
		return ImageMetadata{}, fmt.Errorf("%w: %s must be %q or %q, got %q",
			ErrInvalidImage, LabelImageKind, ImageKindBase, ImageKindEnvironment, m.Kind)
	}
	if err := requireLabels(labels, LabelImageInputs, LabelImagePostgresClient, LabelImageBase); err != nil {
		return ImageMetadata{}, err
	}
	if !inputsRe.MatchString(labels[LabelImageInputs]) {
		return ImageMetadata{}, fmt.Errorf("%w: %s must be sha256:<64 hex>, got %q", ErrInvalidImage, LabelImageInputs, labels[LabelImageInputs])
	}
	if m.PostgresClient, err = canonicalInt(labels, LabelImagePostgresClient); err != nil {
		return ImageMetadata{}, err
	}
	m.Inputs, m.Base = labels[LabelImageInputs], labels[LabelImageBase]
	return m, nil
}

func requireLabels(labels map[string]string, keys ...string) error {
	for _, key := range keys {
		if labels[key] == "" {
			return fmt.Errorf("%w: label %s is missing", ErrInvalidImage, key)
		}
	}
	return nil
}

func canonicalInt(labels map[string]string, key string) (int, error) {
	value, err := strconv.Atoi(labels[key])
	if err != nil || value <= 0 || strconv.Itoa(value) != labels[key] {
		return 0, fmt.Errorf("%w: %s must be a positive integer, got %q", ErrInvalidImage, key, labels[key])
	}
	return value, nil
}

// ErrImageNotFound reports that a referenced image is not present in the
// runtime. Images are never pulled implicitly (ADR 0005 A1).
var ErrImageNotFound = errors.New("image not found")

// ErrImageBuild reports a build that the builder ran and that failed, such as
// a failing step, as opposed to an unreachable runtime.
var ErrImageBuild = errors.New("image build failed")

// ImageInfo is what image inspection reports.
type ImageInfo struct {
	ID          string
	RepoDigests []string
	Labels      map[string]string
	OS          string
	Arch        string
}

// pinnedRe matches a reference that names its content: name@sha256:<digest>.
var pinnedRe = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)

// IsPinned reports whether ref is pinned by digest. Only pinned references
// are pulled: a tag is never an identity (ADR 0008 §13).
func IsPinned(ref string) bool { return pinnedRe.MatchString(ref) }

// ImageStore inspects, pulls, and tags images in a runtime. Pulls are always
// explicit operations, never a side effect of another primitive.
type ImageStore interface {
	// InspectImage returns ErrImageNotFound when ref is absent.
	InspectImage(ctx context.Context, ref string) (ImageInfo, error)
	// PullImage pulls a reference pinned by digest, writing progress lines.
	PullImage(ctx context.Context, ref string, progress io.Writer) error
	// TagImage adds target as a name of the image source designates.
	TagImage(ctx context.Context, source, target string) error
}

// BuildRequest describes one image build. Context is a tar stream with the
// Dockerfile at its root. Every FROM must already be present in the runtime;
// callers inspect bases first because builders pull missing ones.
type BuildRequest struct {
	Context io.Reader
	Tags    []string
	// Labels are applied to the final image by the builder, so metadata
	// derived from the Dockerfile itself never has to be written into it.
	Labels   map[string]string
	Progress io.Writer
}

// BuiltImage is a build result and the engine that holds it. It reaches the
// runtime that runs it through an explicit delivery step (ADR 0001 §4).
type BuiltImage struct {
	ID     string
	Engine EngineIdentity
}

// ImageBuilder builds images. It is separate from Engine so that building and
// execution can be provided by different components.
type ImageBuilder interface {
	BuildImage(ctx context.Context, req BuildRequest) (BuiltImage, error)
}

// PlatformArch maps an architecture name onto its OCI platform name. Engines
// report the kernel's name (Docker's Info gives aarch64 or x86_64) while
// images carry the OCI one (arm64, amd64); compare them only through this.
// Unknown names are returned unchanged.
func PlatformArch(arch string) string {
	switch arch {
	case "x86_64", "x86-64":
		return "amd64"
	case "aarch64", "armv8", "armv8l":
		return "arm64"
	case "armv7l", "armhf":
		return "arm"
	case "i386", "i686":
		return "386"
	}
	return arch
}
