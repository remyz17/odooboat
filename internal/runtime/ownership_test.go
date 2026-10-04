package runtime

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

const (
	testWorkspace   = "11111111-1111-4111-8111-111111111111"
	testEnvironment = "3f9a2c1e-2222-4222-8222-222222222222"
	testOperation   = "33333333-3333-4333-8333-333333333333"
)

func environmentOwnership(role Role) Ownership {
	return Ownership{
		Scope: ScopeEnvironment, WorkspaceID: testWorkspace, EnvironmentID: testEnvironment,
		Role: role, OperationID: testOperation, DisplayProject: "acme",
		DisplayEnvironment: "default", CreatedBy: "odooboat/dev",
	}
}

func TestOwnershipRoundTrip(t *testing.T) {
	postgresData := environmentOwnership(RolePostgresData)
	postgresData.PostgresMajor = 16
	odoo := environmentOwnership(RoleOdoo)
	odoo.Spec = "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name string
		t    ResourceType
		o    Ownership
	}{
		{"postgres container", ResourceContainer, environmentOwnership(RolePostgres)},
		{"odoo container with spec", ResourceContainer, odoo},
		{"job container", ResourceContainer, environmentOwnership(RoleJob)},
		{"postgres data volume", ResourceVolume, postgresData},
		{"odoo data volume", ResourceVolume, environmentOwnership(RoleOdooData)},
		{"environment network", ResourceNetwork, environmentOwnership(RoleEnvironment)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			labels, err := tc.o.Labels(tc.t)
			if err != nil {
				t.Fatal(err)
			}
			if labels[LabelSchema] != "1" {
				t.Fatalf("schema label = %q", labels[LabelSchema])
			}
			labels["com.example.user"] = "kept by the user"
			claim := DecodeOwnership(tc.t, labels)
			if claim.State != ClaimValid || claim.Err != nil || claim.Ownership != tc.o {
				t.Fatalf("claim = %+v, want %+v", claim, tc.o)
			}
		})
	}
}

func TestDecodeUnmanaged(t *testing.T) {
	for _, labels := range []map[string]string{nil, {}, {"com.docker.compose.project": "acme", "odooboat": "x"}} {
		if claim := DecodeOwnership(ResourceContainer, labels); claim.State != ClaimUnmanaged {
			t.Errorf("labels %v: claim = %+v", labels, claim)
		}
	}
}

func TestDecodeNewerSchemaToleratesUnknownKeys(t *testing.T) {
	labels := map[string]string{LabelSchema: "2", "odooboat.future": "x"}
	claim := DecodeOwnership(ResourceContainer, labels)
	if claim.State != ClaimNewerSchema || claim.Schema != 2 {
		t.Fatalf("claim = %+v", claim)
	}
}

func TestDecodeMalformed(t *testing.T) {
	valid, err := environmentOwnership(RolePostgres).Labels(ResourceContainer)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		t      ResourceType
		mutate func(map[string]string)
	}{
		{"missing schema", ResourceContainer, func(l map[string]string) { delete(l, LabelSchema) }},
		{"non canonical schema", ResourceContainer, func(l map[string]string) { l[LabelSchema] = "01" }},
		{"spaced schema", ResourceContainer, func(l map[string]string) { l[LabelSchema] = " 1" }},
		{"zero schema", ResourceContainer, func(l map[string]string) { l[LabelSchema] = "0" }},
		{"unknown key", ResourceContainer, func(l map[string]string) { l["odooboat.data.other"] = "x" }},
		{"unknown scope", ResourceContainer, func(l map[string]string) { l[LabelScope] = "global" }},
		{"uppercase workspace", ResourceContainer, func(l map[string]string) { l[LabelWorkspace] = "AAAAAAAA-1111-4111-8111-111111111111" }},
		{"missing environment", ResourceContainer, func(l map[string]string) { delete(l, LabelEnvironment) }},
		{"empty spec", ResourceContainer, func(l map[string]string) { l[LabelSpec] = "" }},
		{"bad spec", ResourceContainer, func(l map[string]string) { l[LabelSpec] = "sha256:xyz" }},
		{"role of another type", ResourceVolume, func(map[string]string) {}},
		{"unknown role", ResourceContainer, func(l map[string]string) { l[LabelRole] = "redis" }},
		{"bad operation", ResourceContainer, func(l map[string]string) { l[LabelOperation] = "op-1" }},
		{"postgres major on container", ResourceContainer, func(l map[string]string) { l[LabelPostgresMajor] = "16" }},
		{"control character", ResourceContainer, func(l map[string]string) { l[LabelDisplayProject] = "acme\n" }},
		{"long display", ResourceContainer, func(l map[string]string) { l[LabelDisplayProject] = strings.Repeat("a", 129) }},
		{"bad created-by", ResourceContainer, func(l map[string]string) { l[LabelCreatedBy] = "other/1.0" }},
		{"platform with environment", ResourceContainer, func(l map[string]string) { l[LabelScope] = string(ScopePlatform) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			labels := maps.Clone(valid)
			tc.mutate(labels)
			claim := DecodeOwnership(tc.t, labels)
			if claim.State != ClaimMalformed || !errors.Is(claim.Err, ErrInvalidOwnership) {
				t.Fatalf("claim = %+v", claim)
			}
		})
	}
}

func TestPostgresMajorRequiredOnDataVolume(t *testing.T) {
	if _, err := environmentOwnership(RolePostgresData).Labels(ResourceVolume); !errors.Is(err, ErrInvalidOwnership) {
		t.Fatalf("missing major: err = %v", err)
	}
	o := environmentOwnership(RolePostgresData)
	o.PostgresMajor = 16
	labels, err := o.Labels(ResourceVolume)
	if err != nil {
		t.Fatal(err)
	}
	labels[LabelPostgresMajor] = "016"
	if claim := DecodeOwnership(ResourceVolume, labels); claim.State != ClaimMalformed {
		t.Fatalf("non canonical major: claim = %+v", claim)
	}
}

func TestPlatformScope(t *testing.T) {
	o := Ownership{
		Scope: ScopePlatform, WorkspaceID: testWorkspace, Role: RoleEnvironment,
		OperationID: testOperation, DisplayProject: "acme", CreatedBy: "odooboat/0.4.0",
	}
	labels, err := o.Labels(ResourceNetwork)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := labels[LabelEnvironment]; ok {
		t.Fatal("platform scope must not carry an environment label")
	}
	if claim := DecodeOwnership(ResourceNetwork, labels); claim.State != ClaimValid || claim.Ownership != o {
		t.Fatalf("claim = %+v", claim)
	}
}
