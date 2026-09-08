package config

import (
	"fmt"
	"sort"
	"strings"
)

type SourceKind string

const (
	SourceDefault     SourceKind = "default"
	SourceUser        SourceKind = "user"
	SourceProject     SourceKind = "project"
	SourceEnvironment SourceKind = "environment"
	SourceLocal       SourceKind = "local"
	SourceInvocation  SourceKind = "invocation"
)

// SourceRef locates the authored origin of an effective value.
type SourceRef struct {
	Kind   SourceKind `yaml:"kind" json:"kind"`
	Path   string     `yaml:"path,omitempty" json:"path,omitempty"`
	Field  FieldPath  `yaml:"field,omitempty" json:"field,omitempty"`
	Line   int        `yaml:"line,omitempty" json:"line,omitempty"`
	Column int        `yaml:"column,omitempty" json:"column,omitempty"`
}

func (r SourceRef) String() string {
	var b strings.Builder
	b.WriteString(string(r.Kind))
	if r.Path != "" {
		fmt.Fprintf(&b, " %s", r.Path)
		if r.Line > 0 {
			fmt.Fprintf(&b, ":%d:%d", r.Line, r.Column)
		}
	}
	if r.Field != "" {
		fmt.Fprintf(&b, " (%s)", r.Field)
	}
	return b.String()
}

// FieldProvenance records where an effective value came from. It never carries a
// resolved secret value.
type FieldProvenance struct {
	Field        FieldPath   `yaml:"field" json:"field"`
	Winner       SourceRef   `yaml:"winner" json:"winner"`
	Overridden   []SourceRef `yaml:"overridden,omitempty" json:"overridden,omitempty"`
	ListReplaced bool        `yaml:"listReplaced,omitempty" json:"listReplaced,omitempty"`
	Secret       bool        `yaml:"secret,omitempty" json:"secret,omitempty"`
}

type Provenance map[FieldPath]FieldProvenance

// record sets the winner for field, demoting any previous winner to the
// overridden chain. Callers must apply sources in ascending precedence order.
func (p Provenance) record(field FieldPath, ref SourceRef) {
	existing, ok := p[field]
	if !ok {
		p[field] = FieldProvenance{Field: field, Winner: ref}
		return
	}
	existing.Overridden = append(existing.Overridden, existing.Winner)
	existing.Winner = ref
	p[field] = existing
}

func (p Provenance) markListReplaced(field FieldPath) {
	entry := p[field]
	entry.ListReplaced = true
	p[field] = entry
}

func (p Provenance) markSecret(field FieldPath) {
	entry := p[field]
	entry.Secret = true
	p[field] = entry
}

func (p Provenance) Lookup(field FieldPath) (FieldProvenance, bool) {
	entry, ok := p[field]
	return entry, ok
}

// Fields returns every recorded field path in stable order.
func (p Provenance) Fields() []FieldPath {
	fields := make([]FieldPath, 0, len(p))
	for field := range p {
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i] < fields[j] })
	return fields
}
