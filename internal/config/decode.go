package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	nullTag  = "!!null"
	intTag   = "!!int"
	mergeTag = "!!merge"
)

type position struct {
	Line   int
	Column int
}

// positionIndex maps a dotted authored path within one document to its position.
type positionIndex map[string]position

type loaded[T any] struct {
	kind      SourceKind
	path      string
	dir       string
	doc       T
	positions positionIndex
}

// docRef resolves authored paths inside one document to source references. The
// kind may differ from the document's own kind: environment overlays live in the
// project file but are attributed to SourceEnvironment.
type docRef struct {
	kind      SourceKind
	path      string
	dir       string
	positions positionIndex
}

func (d docRef) ref(authored string) SourceRef {
	pos := d.positions[authored]
	return SourceRef{
		Kind:   d.kind,
		Path:   d.path,
		Field:  FieldPath(authored),
		Line:   pos.Line,
		Column: pos.Column,
	}
}

func (l *loaded[T]) as(kind SourceKind) docRef {
	if l == nil {
		return docRef{kind: kind}
	}
	return docRef{kind: kind, path: l.path, dir: l.dir, positions: l.positions}
}

// ref locates an authored path inside the document, or the file itself when the
// path was not authored.
func (l *loaded[T]) ref(authored string) SourceRef {
	if l == nil {
		return SourceRef{}
	}
	return l.as(l.kind).ref(authored)
}

func decodeFile[T any](kind SourceKind, path string) (*loaded[T], error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fileErr(kind, path, ErrNotFound, "file does not exist", "")
		}
		return nil, fileErr(kind, path, ErrNotFound, err.Error(), "")
	}

	root, err := parseSingleDocument(kind, path, data)
	if err != nil {
		return nil, err
	}
	if err := checkNode(kind, path, root, ""); err != nil {
		return nil, err
	}
	// Gate on schema before typed decoding so a future version is never
	// reinterpreted against today's model.
	if err := checkSchema(kind, path, root); err != nil {
		return nil, err
	}

	var doc T
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, decodeErr(kind, path, err)
	}

	positions := positionIndex{}
	indexPositions(root, "", positions)
	return &loaded[T]{
		kind:      kind,
		path:      path,
		dir:       filepath.Dir(path),
		doc:       doc,
		positions: positions,
	}, nil
}

func parseSingleDocument(kind SourceKind, path string, data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fileErr(kind, path, ErrDecode, "file contains no YAML document",
				fmt.Sprintf("author a document starting with \"schema: %d\"", SchemaVersion))
		}
		return nil, decodeErr(kind, path, err)
	}

	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, nodeErr(ErrDecode, kind, path, &extra, "",
			"file contains more than one YAML document", "keep exactly one document per file")
	case errors.Is(err, io.EOF):
	default:
		return nil, decodeErr(kind, path, err)
	}

	content := &root
	if content.Kind == yaml.DocumentNode && len(content.Content) == 1 {
		content = content.Content[0]
	}
	if content.Kind != yaml.MappingNode {
		return nil, nodeErr(ErrDecode, kind, path, content, "",
			"document must be a mapping", fmt.Sprintf("start the file with \"schema: %d\"", SchemaVersion))
	}
	return content, nil
}

func checkNode(kind SourceKind, path string, n *yaml.Node, field string) error {
	switch n.Kind {
	case yaml.AliasNode:
		return nodeErr(ErrDecode, kind, path, n, field,
			"YAML aliases are not supported", "write the value explicitly")

	case yaml.MappingNode:
		seen := make(map[string]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Tag == mergeTag || key.Value == "<<" {
				return nodeErr(ErrDecode, kind, path, key, field,
					"YAML merge keys are not supported", "write inherited values explicitly")
			}
			if key.Kind != yaml.ScalarNode {
				return nodeErr(ErrDecode, kind, path, key, field, "mapping keys must be scalars", "")
			}
			child := joinField(field, key.Value)
			if line, dup := seen[key.Value]; dup {
				return nodeErr(ErrDecode, kind, path, key, child,
					fmt.Sprintf("duplicate mapping key, already defined at line %d", line),
					"remove one of the definitions")
			}
			seen[key.Value] = key.Line
			if isExplicitNull(value) {
				return nodeErr(ErrDecode, kind, path, value, child, "explicit null is not supported",
					"omit the field to inherit it, or author a value")
			}
			if err := checkNode(kind, path, value, child); err != nil {
				return err
			}
		}

	case yaml.SequenceNode:
		for i, item := range n.Content {
			child := fmt.Sprintf("%s[%d]", field, i)
			if isExplicitNull(item) {
				return nodeErr(ErrDecode, kind, path, item, child,
					"explicit null is not supported", "remove the list entry")
			}
			if err := checkNode(kind, path, item, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkSchema(kind SourceKind, path string, root *yaml.Node) error {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "schema" {
			continue
		}
		value := root.Content[i+1]
		version, err := strconv.Atoi(value.Value)
		if err != nil || value.Tag != intTag {
			return nodeErr(ErrAuthored, kind, path, value, "schema", "schema must be an integer",
				fmt.Sprintf("use \"schema: %d\"", SchemaVersion))
		}
		if version != SchemaVersion {
			return nodeErr(ErrAuthored, kind, path, value, "schema",
				fmt.Sprintf("unsupported schema version %d", version),
				fmt.Sprintf("this build of odooboat supports schema %d", SchemaVersion))
		}
		return nil
	}
	return fileErr(kind, path, ErrAuthored, `required field "schema" is missing`,
		fmt.Sprintf("add \"schema: %d\" at the top of the file", SchemaVersion))
}

func indexPositions(n *yaml.Node, field string, index positionIndex) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			child := joinField(field, key.Value)
			index[child] = position{Line: key.Line, Column: key.Column}
			indexPositions(value, child, index)
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			child := fmt.Sprintf("%s[%d]", field, i)
			index[child] = position{Line: item.Line, Column: item.Column}
			indexPositions(item, child, index)
		}
	}
}

func isExplicitNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == nullTag
}

func joinField(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func nodeErr(class error, kind SourceKind, path string, n *yaml.Node, field, rule, hint string) error {
	return fieldErr(class, FieldPath(field), SourceRef{
		Kind:   kind,
		Path:   path,
		Field:  FieldPath(field),
		Line:   n.Line,
		Column: n.Column,
	}, rule, "", hint)
}

var (
	yamlLineRe     = regexp.MustCompile(`^line (\d+): (.*)$`)
	unknownFieldRe = regexp.MustCompile(`^field (\S+) not found in type \S+$`)
)

func decodeErr(kind SourceKind, path string, err error) error {
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		c := &collector{}
		for _, msg := range typeErr.Errors {
			c.add(yamlMessageErr(kind, path, msg))
		}
		return c.err()
	}
	return yamlMessageErr(kind, path, err.Error())
}

func yamlMessageErr(kind SourceKind, path, msg string) error {
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n  ")
	line := 0
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		line, _ = strconv.Atoi(m[1])
		msg = m[2]
	}
	hint := ""
	if m := unknownFieldRe.FindStringSubmatch(msg); m != nil {
		msg = fmt.Sprintf("unknown field %q", m[1])
		hint = "remove it, or author it in the document that owns this field"
	}
	return fieldErr(ErrDecode, "", SourceRef{Kind: kind, Path: path, Line: line}, msg, "", hint)
}
