package runtime

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

func baseMetadata() ImageMetadata {
	return ImageMetadata{Contract: ImageContract, Series: "18.0", Python: "3.12", Kind: ImageKindBase}
}

func TestImageMetadataRoundTrip(t *testing.T) {
	labels := baseMetadata().Labels()
	// Unrelated labels and unknown image metadata are tolerated.
	labels["org.opencontainers.image.source"] = "https://example.invalid/odooboat"
	labels[ImageMetadataPrefix+"inputs"] = "sha256:abc"
	got, err := DecodeImageMetadata(labels)
	if err != nil || got != baseMetadata() {
		t.Fatalf("decode = %+v, %v", got, err)
	}
}

func TestDecodeImageMetadataRejects(t *testing.T) {
	cases := map[string]func(map[string]string){
		"missing series":    func(l map[string]string) { delete(l, LabelImageSeries) },
		"empty python":      func(l map[string]string) { l[LabelImagePython] = "" },
		"contract not int":  func(l map[string]string) { l[LabelImageContract] = "one" },
		"contract padded":   func(l map[string]string) { l[LabelImageContract] = "01" },
		"future contract":   func(l map[string]string) { l[LabelImageContract] = "2" },
		"unknown kind":      func(l map[string]string) { l[LabelImageKind] = "agent" },
		"ownership claimed": func(l map[string]string) { l[LabelWorkspace] = "w" },
	}
	for name, mutate := range cases {
		labels := maps.Clone(baseMetadata().Labels())
		mutate(labels)
		if _, err := DecodeImageMetadata(labels); !errors.Is(err, ErrInvalidImage) {
			t.Errorf("%s: err = %v, want ErrInvalidImage", name, err)
		}
	}
}

// Containers created from an Odooboat image inherit its metadata and remain
// unmanaged until Odooboat labels them (ADR 0008 verification requirements).
func TestImageMetadataDoesNotClaimOwnership(t *testing.T) {
	if claim := DecodeOwnership(ResourceContainer, baseMetadata().Labels()); claim.State != ClaimUnmanaged {
		t.Fatalf("claim = %+v", claim)
	}
}

func environmentMetadata() ImageMetadata {
	return ImageMetadata{Contract: ImageContract, Series: "18.0", Python: "3.12", Kind: ImageKindEnvironment,
		Inputs: "sha256:" + strings.Repeat("ab", 32), PostgresClient: 16, Base: "ghcr.io/x/odoo@sha256:" + strings.Repeat("cd", 32)}
}

func TestEnvironmentMetadataRoundTrip(t *testing.T) {
	got, err := DecodeImageMetadata(environmentMetadata().Labels())
	if err != nil || got != environmentMetadata() {
		t.Fatalf("decode = %+v, %v", got, err)
	}
	// Environment keys on a base image are not part of its metadata.
	labels := baseMetadata().Labels()
	labels[LabelImageInputs] = "garbage"
	if got, err := DecodeImageMetadata(labels); err != nil || got != baseMetadata() {
		t.Fatalf("base with stray keys = %+v, %v", got, err)
	}
}

func TestDecodeEnvironmentMetadataRejects(t *testing.T) {
	cases := map[string]func(map[string]string){
		"missing inputs":      func(l map[string]string) { delete(l, LabelImageInputs) },
		"inputs not a digest": func(l map[string]string) { l[LabelImageInputs] = "sha256:abc" },
		"client not int":      func(l map[string]string) { l[LabelImagePostgresClient] = "16.1" },
		"client zero":         func(l map[string]string) { l[LabelImagePostgresClient] = "0" },
		"missing base":        func(l map[string]string) { delete(l, LabelImageBase) },
	}
	for name, mutate := range cases {
		labels := environmentMetadata().Labels()
		mutate(labels)
		if _, err := DecodeImageMetadata(labels); !errors.Is(err, ErrInvalidImage) {
			t.Errorf("%s: err = %v, want ErrInvalidImage", name, err)
		}
	}
}

func TestIsPinned(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("0", 64)
	for ref, want := range map[string]bool{
		"ghcr.io/x/odoo" + digest:           true,
		"ghcr.io/x/odoo:18.0" + digest:      true,
		"ghcr.io/x/odoo:18.0":               false,
		"sha256:" + strings.Repeat("0", 64): false,
		"ghcr.io/x/odoo@sha256:abc":         false,
	} {
		if IsPinned(ref) != want {
			t.Errorf("IsPinned(%q) = %v", ref, !want)
		}
	}
}

func TestPlatformArch(t *testing.T) {
	for in, want := range map[string]string{
		"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64",
		"armv7l": "arm", "i686": "386", "s390x": "s390x", "": "",
	} {
		if got := PlatformArch(in); got != want {
			t.Errorf("PlatformArch(%q) = %q, want %q", in, got, want)
		}
	}
}
