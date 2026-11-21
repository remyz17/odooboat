package config

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveSecret(t *testing.T) {
	env := map[string]string{"SET": "from-env", "EMPTY": ""}
	lookup := func(name string) (string, bool) { v, ok := env[name]; return v, ok }

	cases := map[string]struct {
		secret     ResolvedSecret
		value      string
		configured bool
		err        bool
	}{
		"none":         {ResolvedSecret{}, "", false, false},
		"literal":      {newLiteralSecret("lit"), "lit", true, false},
		"env set":      {newEnvSecret("SET"), "from-env", true, false},
		"env unset":    {newEnvSecret("UNSET"), "", true, true},
		"env empty":    {newEnvSecret("EMPTY"), "", true, true},
		"unknown kind": {ResolvedSecret{Kind: "vault"}, "", false, true},
	}
	for name, tc := range cases {
		value, configured, err := tc.secret.Resolve(lookup)
		if value != tc.value || configured != tc.configured || (err != nil) != tc.err {
			t.Errorf("%s: Resolve = %q, %v, %v", name, value, configured, err)
		}
		if err != nil && !errors.Is(err, ErrResolved) {
			t.Errorf("%s: err = %v, want ErrResolved", name, err)
		}
	}
	// The error names the variable, never a value.
	_, _, err := newEnvSecret("UNSET").Resolve(lookup)
	if !strings.Contains(err.Error(), "UNSET") {
		t.Fatalf("err = %v", err)
	}
}
