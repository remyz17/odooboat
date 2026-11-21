package envspec

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/runtime"
)

var update = flag.Bool("update", false, "rewrite golden files")

func inputs() Inputs {
	return Inputs{
		Config: config.ResolvedConfig{
			ProjectName: "acme", EnvironmentName: "default", OdooVersion: "18.0", Python: "3.12", Postgres: 16,
			Addons: []config.ResolvedAddon{
				{Name: "core", Path: "/work/acme/addons/core", Enabled: true},
				{Name: "extra", Path: "/work/acme/addons/extra", Enabled: false},
			},
			Odoo: config.ResolvedOdoo{
				Options: map[string]string{"workers": "0", "limit_time_cpu": "120"},
				Source: config.ResolvedOdooSource{Mode: config.SourceModeMount,
					Community:  &config.ResolvedSourceLocation{Path: "/work/odoo/community"},
					Enterprise: &config.ResolvedSourceLocation{Path: "/work/odoo/enterprise"}},
			},
		},
		Identity: Identity{
			WorkspaceID: "11111111-1111-4111-8111-111111111111", EnvironmentID: "3f9a2c1e-2222-4222-8222-222222222222",
			Project: "acme", Environment: "default", OperationID: "33333333-3333-4333-8333-333333333333", Version: "test",
		},
		Images:         Images{Odoo: "sha256:" + strings.Repeat("a", 64), Postgres: "sha256:" + strings.Repeat("b", 64)},
		MasterPassword: "s3cret", Configured: true,
	}
}

func mustNew(t *testing.T, in Inputs) Desired {
	t.Helper()
	d, err := New(in)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/envspec -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs; run go test ./internal/envspec -update and review:\n%s", path, got)
	}
}

func TestDesiredGolden(t *testing.T) {
	d := mustNew(t, inputs())
	golden(t, "odoo.conf", d.Odoo.Files[0].Content)
	view := map[string]any{"network": d.Network, "volumes": d.Volumes, "postgres": d.Postgres.Spec, "odoo": d.Odoo.Spec,
		"postgresHash": d.Postgres.Hash, "odooHash": d.Odoo.Hash, "warnings": d.Warnings}
	encoded, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "desired.json", append(encoded, '\n'))
}

func TestResourcesCarryValidOwnership(t *testing.T) {
	d := mustNew(t, inputs())
	check := func(kind runtime.ResourceType, labels map[string]string, role runtime.Role) runtime.Ownership {
		t.Helper()
		claim := runtime.DecodeOwnership(kind, labels)
		if claim.State != runtime.ClaimValid || claim.Ownership.Role != role {
			t.Fatalf("%s %s: claim %+v", kind, role, claim)
		}
		return claim.Ownership
	}
	check(runtime.ResourceNetwork, d.Network.Labels, runtime.RoleEnvironment)
	if o := check(runtime.ResourceVolume, d.Volumes[0].Labels, runtime.RolePostgresData); o.PostgresMajor != 16 {
		t.Fatalf("postgres-data fact = %d", o.PostgresMajor)
	}
	check(runtime.ResourceVolume, d.Volumes[1].Labels, runtime.RoleOdooData)
	for _, c := range d.Containers() {
		if o := check(runtime.ResourceContainer, c.Spec.Labels, c.Role); o.Spec != c.Hash {
			t.Fatalf("%s spec label %q != hash %q", c.Role, o.Spec, c.Hash)
		}
	}
	if !strings.HasSuffix(d.Odoo.Spec.Name, "-odoo-3f9a2c1e") || d.Network.Name != "ob-acme-default-environment-3f9a2c1e" {
		t.Fatalf("names: %s, %s", d.Odoo.Spec.Name, d.Network.Name)
	}
}

// The hash follows what the container runs, not who created it.
func TestSpecHash(t *testing.T) {
	reference := mustNew(t, inputs())
	same := inputs()
	same.Identity.OperationID = "44444444-4444-4444-8444-444444444444"
	same.Identity.Version = "other"
	if d := mustNew(t, same); d.Odoo.Hash != reference.Odoo.Hash || d.Postgres.Hash != reference.Postgres.Hash {
		t.Fatal("the operation or version changed the spec hash")
	}
	changes := map[string]func(*Inputs){
		"option":          func(in *Inputs) { in.Config.Odoo.Options = map[string]string{"workers": "2"} },
		"odoo image":      func(in *Inputs) { in.Images.Odoo = "sha256:" + strings.Repeat("c", 64) },
		"master password": func(in *Inputs) { in.MasterPassword = "other" },
		"addon enabled": func(in *Inputs) {
			in.Config.Addons = []config.ResolvedAddon{{Name: "core", Path: "/work/acme/addons/core", Enabled: true},
				{Name: "extra", Path: "/work/acme/addons/extra", Enabled: true}}
		},
		"community path": func(in *Inputs) {
			in.Config.Odoo.Source.Community = &config.ResolvedSourceLocation{Path: "/elsewhere"}
		},
	}
	for name, change := range changes {
		in := inputs()
		change(&in)
		d := mustNew(t, in)
		if d.Odoo.Hash == reference.Odoo.Hash {
			t.Errorf("%s did not change the odoo hash", name)
		}
		if d.Postgres.Hash != reference.Postgres.Hash {
			t.Errorf("%s changed the postgres hash", name)
		}
	}
	in := inputs()
	in.Images.Postgres = "sha256:" + strings.Repeat("d", 64)
	if mustNew(t, in).Postgres.Hash == reference.Postgres.Hash {
		t.Error("the postgres image did not change its hash")
	}
}

func TestEmbeddedSourcesAreNotMounted(t *testing.T) {
	in := inputs()
	in.Config.Odoo.Source = config.ResolvedOdooSource{Mode: config.SourceModeEmbedded,
		Community: &config.ResolvedSourceLocation{Git: "https://example.com/odoo.git", Commit: strings.Repeat("e", 40)}}
	in.Config.Addons = nil
	d := mustNew(t, in)
	if d.NeedsBindMounts() {
		t.Fatalf("embedded environment needs bind mounts: %+v", d.Odoo.Spec.Mounts)
	}
	conf := string(d.Odoo.Files[0].Content)
	if !strings.Contains(conf, "addons_path = /opt/odoo/community/addons,/opt/odoo/community/odoo/addons\n") {
		t.Fatalf("addons_path without enterprise:\n%s", conf)
	}
}

func TestWarnings(t *testing.T) {
	in := inputs()
	in.Configured, in.MasterPassword = false, ""
	in.Config.Odoo.Options = map[string]string{"workers": "2"}
	d := mustNew(t, in)
	if len(d.Warnings) != 2 || strings.Contains(string(d.Odoo.Files[0].Content), "admin_passwd") {
		t.Fatalf("warnings = %q\n%s", d.Warnings, d.Odoo.Files[0].Content)
	}
}

func TestArchive(t *testing.T) {
	d := mustNew(t, inputs())
	data, err := Archive(d.Odoo.Files)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	header, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(tr)
	if header.Name != ConfFile || header.Mode != 0o640 || header.Uid != 0 || header.Gid != 0 || !bytes.Equal(content, d.Odoo.Files[0].Content) {
		t.Fatalf("header %+v", header)
	}
}

func TestNewRejects(t *testing.T) {
	in := inputs()
	in.Config.Odoo.Source.Community = nil
	if _, err := New(in); err == nil {
		t.Error("no community source accepted")
	}
	in = inputs()
	in.Images.Postgres = ""
	if _, err := New(in); err == nil {
		t.Error("no postgres image accepted")
	}
}
