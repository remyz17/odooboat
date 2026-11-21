package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/remyz17/odooboat/images"
)

// PostgresImagesPath locates the PostgreSQL server images in the repository.
const PostgresImagesPath = "images/postgres.yaml"

type postgresDocument struct {
	Schema int               `yaml:"schema"`
	Images map[string]string `yaml:"images"`
}

// PostgresImages maps a PostgreSQL major version to its server image, pinned
// by digest (ADR 0009 §2).
type PostgresImages struct {
	images map[int]string
}

// Lookup returns the server image of a major version.
func (p PostgresImages) Lookup(major int) (string, bool) {
	image, ok := p.images[major]
	return image, ok
}

// Majors lists the major versions with an image, ascending.
func (p PostgresImages) Majors() []int {
	majors := make([]int, 0, len(p.images))
	for major := range p.images {
		majors = append(majors, major)
	}
	slices.Sort(majors)
	return majors
}

var embeddedPostgres = sync.OnceValues(func() (PostgresImages, error) {
	matrix, err := EmbeddedMatrix()
	if err != nil {
		return PostgresImages{}, err
	}
	return ParsePostgresImages(images.Postgres, matrix)
})

// EmbeddedPostgresImages returns the server images compiled into this binary.
func EmbeddedPostgresImages() (PostgresImages, error) { return embeddedPostgres() }

// ParsePostgresImages strictly decodes the server images. Every major version
// the matrix supports must have one, and each reference must be pinned by
// digest and tagged with its own major, which catches a misplaced line.
func ParsePostgresImages(data []byte, matrix Matrix) (PostgresImages, error) {
	doc, err := decodeBytes[postgresDocument](SourcePostgresImages, PostgresImagesPath, data)
	if err != nil {
		return PostgresImages{}, err
	}
	ref := doc.as(SourcePostgresImages)
	c := &collector{}
	if doc.doc.Schema != 1 {
		c.addField(ErrAuthored, "schema", ref.ref("schema"), "unsupported postgres images schema", fmt.Sprint(doc.doc.Schema), "this binary reads schema 1")
	}
	supported := map[int]bool{}
	for _, series := range matrix.Series() {
		entry, _ := matrix.Lookup(series)
		for _, major := range entry.Postgres {
			supported[major] = true
		}
	}
	p := PostgresImages{images: map[int]string{}}
	for _, key := range sortedKeys(doc.doc.Images) {
		image := doc.doc.Images[key]
		field := "images." + key
		major, err := strconv.Atoi(key)
		switch {
		case err != nil || strconv.Itoa(major) != key || major <= 0:
			c.addField(ErrAuthored, FieldPath(field), ref.ref(field), "a key must be a PostgreSQL major version", key, "")
		case !supported[major]:
			c.addField(ErrAuthored, FieldPath(field), ref.ref(field), "major version is not supported by the matrix", key, "")
		case !publishedRe.MatchString(image):
			c.addField(ErrAuthored, FieldPath(field), ref.ref(field), "a server image must be pinned by digest", image, "")
		case !strings.Contains(image, ":"+key+"-") && !strings.Contains(image, ":"+key+"@"):
			c.addField(ErrAuthored, FieldPath(field), ref.ref(field), "the image tag must name the same major version", image, "")
		default:
			p.images[major] = image
		}
	}
	for major := range supported {
		if _, ok := doc.doc.Images[strconv.Itoa(major)]; !ok {
			c.addField(ErrAuthored, "images", ref.ref("images"), "a major version supported by the matrix has no image", strconv.Itoa(major), "")
		}
	}
	if err := c.err(); err != nil {
		return PostgresImages{}, err
	}
	return p, nil
}
