package config

import (
	"encoding/json"
	"fmt"
)

// RedactedPlaceholder replaces every secret value in output, provenance and errors.
const RedactedPlaceholder = "<redacted>"

// Redact returns the placeholder for any non-empty value.
func Redact(value string) string {
	if value == "" {
		return ""
	}
	return RedactedPlaceholder
}

type SecretKind string

const (
	SecretKindNone    SecretKind = ""
	SecretKindEnv     SecretKind = "env"
	SecretKindLiteral SecretKind = "literal"
)

// ResolvedSecret keeps the secret value unexported so it cannot be serialized by
// accident. Its marshallers emit metadata only.
type ResolvedSecret struct {
	Kind  SecretKind
	Ref   string
	value string
}

func newEnvSecret(name string) ResolvedSecret {
	return ResolvedSecret{Kind: SecretKindEnv, Ref: name}
}

func newLiteralSecret(value string) ResolvedSecret {
	return ResolvedSecret{Kind: SecretKindLiteral, value: value}
}

func (s ResolvedSecret) IsZero() bool { return s.Kind == SecretKindNone }

// Value exposes the authored literal. Env-backed secrets are read by Resolve,
// at the moment an operation needs them.
func (s ResolvedSecret) Value() (string, bool) {
	return s.value, s.Kind == SecretKindLiteral
}

// Resolve returns the secret's value, reading an env-backed secret through
// lookup (os.LookupEnv in production). It reports whether a secret is
// configured at all. A configured env secret whose variable is unset or
// empty is an error: silently running without it would weaken what the
// author asked for.
func (s ResolvedSecret) Resolve(lookup func(string) (string, bool)) (string, bool, error) {
	switch s.Kind {
	case SecretKindNone:
		return "", false, nil
	case SecretKindLiteral:
		return s.value, true, nil
	case SecretKindEnv:
		value, ok := lookup(s.Ref)
		if !ok || value == "" {
			return "", true, fmt.Errorf("%w: secret environment variable %s is not set", ErrResolved, s.Ref)
		}
		return value, true, nil
	default:
		return "", false, fmt.Errorf("%w: unknown secret kind %q", ErrResolved, s.Kind)
	}
}

type secretView struct {
	Source SecretKind `yaml:"source" json:"source"`
	Env    string     `yaml:"env,omitempty" json:"env,omitempty"`
	Value  string     `yaml:"value,omitempty" json:"value,omitempty"`
}

func (s ResolvedSecret) view() any {
	switch s.Kind {
	case SecretKindEnv:
		return secretView{Source: SecretKindEnv, Env: s.Ref}
	case SecretKindLiteral:
		return secretView{Source: SecretKindLiteral, Value: RedactedPlaceholder}
	default:
		return nil
	}
}

func (s ResolvedSecret) MarshalYAML() (any, error) { return s.view(), nil }

func (s ResolvedSecret) MarshalJSON() ([]byte, error) { return json.Marshal(s.view()) }
