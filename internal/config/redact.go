package config

import "encoding/json"

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

// Value exposes the authored literal. Credential resolution for env-backed
// secrets is deferred to a later slice.
func (s ResolvedSecret) Value() (string, bool) {
	return s.value, s.Kind == SecretKindLiteral
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
