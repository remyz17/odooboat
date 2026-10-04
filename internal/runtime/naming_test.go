package runtime

import (
	"strings"
	"testing"
)

func TestResourceNameShort(t *testing.T) {
	name, err := ResourceName("acme", "default", RolePostgres, testEnvironment)
	if err != nil || name != "ob-acme-default-postgres-3f9a2c1e" {
		t.Fatalf("name = %q, %v", name, err)
	}
	job, err := JobName("acme", "default", testEnvironment, testOperation)
	if err != nil || job != "ob-acme-default-job-3f9a2c1e-33333333" {
		t.Fatalf("job = %q, %v", job, err)
	}
}

func TestResourceNameSanitizes(t *testing.T) {
	name, err := ResourceName("Acme_Shop", "feature..x_", RoleOdoo, testEnvironment)
	if err != nil || name != "ob-acme-shop-feature-x-odoo-3f9a2c1e" {
		t.Fatalf("name = %q, %v", name, err)
	}
}

func TestResourceNameFitsDNSLabel(t *testing.T) {
	long := strings.Repeat("abcdefghi-", 10)
	inputs := [][2]string{{long, "default"}, {"acme", long}, {long, long}, {"a", long + "z"}}
	for _, in := range inputs {
		for _, role := range []Role{RolePostgresData, RoleEnvironment} {
			name, err := ResourceName(in[0], in[1], role, testEnvironment)
			if err != nil {
				t.Fatalf("%q/%q: %v", in[0], in[1], err)
			}
			if len(name) > MaxNameLength || !dnsLabelRe.MatchString(name) {
				t.Fatalf("name %q is not a DNS label", name)
			}
			if !strings.HasSuffix(name, "-"+string(role)+"-3f9a2c1e") {
				t.Fatalf("name %q lost its role or env8", name)
			}
			again, _ := ResourceName(in[0], in[1], role, testEnvironment)
			if again != name {
				t.Fatalf("name is not deterministic: %q != %q", name, again)
			}
		}
		job, err := JobName(in[0], in[1], testEnvironment, testOperation)
		if err != nil || len(job) > MaxNameLength || !strings.HasSuffix(job, "-3f9a2c1e-33333333") {
			t.Fatalf("job = %q, %v", job, err)
		}
	}
}

func TestResourceNameRejects(t *testing.T) {
	if _, err := ResourceName("___", "default", RoleOdoo, testEnvironment); err == nil {
		t.Error("empty sanitized project accepted")
	}
	if _, err := ResourceName("acme", "default", RoleOdoo, "3f9a2c1e"); err == nil {
		t.Error("non-UUID environment accepted")
	}
	if _, err := JobName("acme", "default", testEnvironment, "op"); err == nil {
		t.Error("non-UUID operation accepted")
	}
}
