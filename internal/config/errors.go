package config

import (
	"errors"
	"fmt"
	"strings"
)

// Error classes. The CLI maps these onto process exit codes.
var (
	ErrNotFound = errors.New("configuration file not found")
	ErrDecode   = errors.New("configuration could not be decoded")
	ErrAuthored = errors.New("authored configuration is invalid")
	ErrResolved = errors.New("resolved configuration is invalid")
)

// FieldError reports one violated rule. Value must already be redacted.
type FieldError struct {
	Field  FieldPath
	Source SourceRef
	Rule   string
	Value  string
	Hint   string

	class error
}

func (e *FieldError) Error() string {
	var b strings.Builder
	if e.Source.Path != "" {
		b.WriteString(e.Source.Path)
		if e.Source.Line > 0 {
			fmt.Fprintf(&b, ":%d:%d", e.Source.Line, e.Source.Column)
		}
		b.WriteString(": ")
	}
	if e.Field != "" {
		fmt.Fprintf(&b, "%s: ", e.Field)
	}
	b.WriteString(e.Rule)
	if e.Value != "" {
		fmt.Fprintf(&b, " (got %q)", e.Value)
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "; %s", e.Hint)
	}
	return b.String()
}

func (e *FieldError) Unwrap() error { return e.class }

// Errors aggregates independent failures so one run can report all of them.
type Errors struct {
	Errs []error
}

func (e *Errors) Error() string {
	switch len(e.Errs) {
	case 0:
		return "configuration is invalid"
	case 1:
		return e.Errs[0].Error()
	}
	lines := make([]string, 0, len(e.Errs)+1)
	lines = append(lines, fmt.Sprintf("%d configuration problems:", len(e.Errs)))
	for _, err := range e.Errs {
		lines = append(lines, "  - "+err.Error())
	}
	return strings.Join(lines, "\n")
}

func (e *Errors) Unwrap() []error { return e.Errs }

// collector gathers independent errors within a single validation stage.
type collector struct {
	errs []error
}

// add flattens nested aggregates so a run reports one flat list of problems.
func (c *collector) add(err error) {
	if err == nil {
		return
	}
	var nested *Errors
	if errors.As(err, &nested) {
		c.errs = append(c.errs, nested.Errs...)
		return
	}
	c.errs = append(c.errs, err)
}

func (c *collector) addField(class error, field FieldPath, src SourceRef, rule, value, hint string) {
	c.errs = append(c.errs, &FieldError{
		Field: field, Source: src, Rule: rule, Value: value, Hint: hint, class: class,
	})
}

func (c *collector) err() error {
	if len(c.errs) == 0 {
		return nil
	}
	return &Errors{Errs: c.errs}
}

func fieldErr(class error, field FieldPath, src SourceRef, rule, value, hint string) error {
	return &FieldError{Field: field, Source: src, Rule: rule, Value: value, Hint: hint, class: class}
}

// fileErr reports a source that could not be read or located.
func fileErr(kind SourceKind, path string, class error, rule, hint string) error {
	return &FieldError{
		Source: SourceRef{Kind: kind, Path: path},
		Rule:   rule,
		Hint:   hint,
		class:  class,
	}
}
