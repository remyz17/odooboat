package envimage

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
)

var update = flag.Bool("update", false, "rewrite golden files")

func baseSpec() Spec {
	return Spec{
		From: "ghcr.io/x/odooboat/odoo@sha256:" + strings.Repeat("1", 64), BaseID: "sha256:" + strings.Repeat("2", 64),
		Arch: "arm64", Series: "18.0", Python: "3.12", PostgresClient: 16,
		Requirements: map[string][]byte{Community: []byte("lxml==5.2.1\n")},
	}
}

func mustPlan(t *testing.T, spec Spec) Plan {
	t.Helper()
	plan, err := NewPlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func embeddedTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"odoo/release.py": "version_info = (18, 0, 0, FINAL, 0, '')\n", "odoo-bin": "#!/usr/bin/env python3\n"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "odoo-bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIdentityIsStable(t *testing.T) {
	a, b := mustPlan(t, baseSpec()), mustPlan(t, baseSpec())
	if a.Identity != b.Identity || !strings.HasPrefix(a.Identity, "sha256:") || len(a.Identity) != len("sha256:")+64 {
		t.Fatalf("identities %q %q", a.Identity, b.Identity)
	}
	if a.Tag() != Repository+":"+strings.TrimPrefix(a.Identity, "sha256:") {
		t.Fatalf("tag = %q", a.Tag())
	}
}

// Every input of ADR 0008 §11 changes the identity; nothing else does.
func TestIdentityFollowsInputs(t *testing.T) {
	reference := mustPlan(t, baseSpec()).Identity
	changes := map[string]func(*Spec){
		"requirements":     func(s *Spec) { s.Requirements = map[string][]byte{Community: []byte("lxml==5.3.0\n")} },
		"postgres client":  func(s *Spec) { s.PostgresClient = 15 },
		"base":             func(s *Spec) { s.BaseID = "sha256:" + strings.Repeat("3", 64) },
		"base reference":   func(s *Spec) { s.From = LocalBaseTag(s.BaseID) },
		"arch":             func(s *Spec) { s.Arch = "amd64" },
		"python":           func(s *Spec) { s.Python = "3.11" },
		"enterprise reqs":  func(s *Spec) { s.Requirements[Enterprise] = []byte("x\n") },
		"embedded source":  func(s *Spec) { s.Embedded = map[string]Embedded{Community: {Git: "g", Commit: "c", Dir: "/tmp"}} },
		"embedded commit":  func(s *Spec) { s.Embedded = map[string]Embedded{Community: {Git: "g", Commit: "d", Dir: "/tmp"}} },
		"embedded git url": func(s *Spec) { s.Embedded = map[string]Embedded{Community: {Git: "h", Commit: "c", Dir: "/tmp"}} },
	}
	seen := map[string]string{reference: "reference"}
	for name, change := range changes {
		spec := baseSpec()
		spec.Requirements = map[string][]byte{Community: spec.Requirements[Community]}
		change(&spec)
		identity := mustPlan(t, spec).Identity
		if previous, ok := seen[identity]; ok {
			t.Errorf("%s gives the same identity as %s", name, previous)
		}
		seen[identity] = name
	}

	// The directory an embedded tree was exported to is not an input, and
	// neither is where a mounted source lives: only its requirements are.
	a := baseSpec()
	a.Embedded = map[string]Embedded{Community: {Git: "g", Commit: "c", Dir: "/one"}}
	b := baseSpec()
	b.Embedded = map[string]Embedded{Community: {Git: "g", Commit: "c", Dir: "/two"}}
	if mustPlan(t, a).Identity != mustPlan(t, b).Identity {
		t.Error("export directory changed the identity")
	}
}

func TestDockerfileGolden(t *testing.T) {
	for name, spec := range map[string]Spec{"mount": baseSpec(), "embedded": func() Spec {
		s := baseSpec()
		s.Requirements[Enterprise] = []byte("x\n")
		s.Embedded = map[string]Embedded{Community: {Git: "g", Commit: "c", Dir: "/c"}, Enterprise: {Git: "g", Commit: "e", Dir: "/e"}}
		return s
	}()} {
		golden := filepath.Join("testdata", name+".Dockerfile")
		got := mustPlan(t, spec).Dockerfile
		if *update {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run go test ./internal/envimage -update)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s Dockerfile differs from %s; run go test ./internal/envimage -update and review", name, golden)
		}
		if strings.Contains(string(got), "syntax=") {
			t.Error("a syntax directive would pull a frontend image")
		}
	}
}

func contextEntries(t *testing.T, plan Plan) (map[string]*tar.Header, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := plan.WriteContext(&buf); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	entries := map[string]*tar.Header{}
	tr := tar.NewReader(&buf)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = header
	}
	return entries, raw
}

func TestWriteContext(t *testing.T) {
	spec := baseSpec()
	spec.Embedded = map[string]Embedded{Community: {Git: "g", Commit: "c", Dir: embeddedTree(t)}}
	plan := mustPlan(t, spec)
	entries, first := contextEntries(t, plan)
	for _, name := range []string{"Dockerfile", helperPath, keyPath, constraintsPath, "requirements/community.txt",
		"sources/community/odoo/release.py", "sources/community/odoo-bin"} {
		if entries[name] == nil {
			t.Errorf("context lacks %s", name)
		}
	}
	if entries["requirements/enterprise.txt"] != nil {
		t.Error("context has enterprise requirements that were not given")
	}
	if mode := entries["sources/community/odoo-bin"].Mode; mode != 0o755 {
		t.Errorf("odoo-bin mode = %o", mode)
	}
	if _, again := contextEntries(t, plan); !bytes.Equal(first, again) {
		t.Error("the context is not deterministic")
	}
}

func TestMetadata(t *testing.T) {
	plan := mustPlan(t, baseSpec())
	labels := plan.Metadata(plan.Spec.From).Labels()
	decoded, err := runtime.DecodeImageMetadata(labels)
	if err != nil || decoded.Inputs != plan.Identity || decoded.Kind != runtime.ImageKindEnvironment || decoded.PostgresClient != 16 {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
}

func TestNewPlanRejects(t *testing.T) {
	cases := map[string]func(*Spec){
		"no base":         func(s *Spec) { s.BaseID = "" },
		"no client":       func(s *Spec) { s.PostgresClient = 0 },
		"no community":    func(s *Spec) { s.Requirements = map[string][]byte{} },
		"unknown edition": func(s *Spec) { s.Requirements["themes"] = nil },
		"embedded no dir": func(s *Spec) { s.Embedded = map[string]Embedded{Community: {Git: "g", Commit: "c"}} },
	}
	for name, change := range cases {
		spec := baseSpec()
		change(&spec)
		if _, err := NewPlan(spec); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
