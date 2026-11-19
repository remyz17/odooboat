package git

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sourceRepo creates a repository with two commits and returns its file URL
// and the commit SHAs, oldest first.
func sourceRepo(t *testing.T) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
	write := func(name, content string, mode os.FileMode) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("odoo/release.py", "version_info = (17, 0, 0, FINAL, 0, '')\n", 0o644)
	write("requirements.txt", "lxml\n", 0o644)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "--quiet", "-m", "first")
	first := gitIn(t, dir, "rev-parse", "HEAD")

	write("odoo/release.py", "version_info = (18, 0, 0, FINAL, 0, '')\n", 0o644)
	write("odoo-bin", "#!/usr/bin/env python3\n", 0o755)
	if err := os.Symlink("odoo/release.py", filepath.Join(dir, "release-link")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "--quiet", "-m", "second")
	second := gitIn(t, dir, "rev-parse", "HEAD")
	return "file://" + dir, []string{first, second}
}

func TestExportPinnedCommit(t *testing.T) {
	url, commits := sourceRepo(t)
	ctx := context.Background()

	// The older commit is not a branch tip: it must still be exported exactly.
	dest := filepath.Join(t.TempDir(), "community")
	if err := Export(ctx, url, commits[0], dest); err != nil {
		t.Fatal(err)
	}
	release, err := os.ReadFile(filepath.Join(dest, "odoo/release.py"))
	if err != nil || !strings.Contains(string(release), "(17, 0") {
		t.Fatalf("release.py = %q, %v", release, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "odoo-bin")); !os.IsNotExist(err) {
		t.Fatalf("file from a later commit exported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
		t.Fatal("exported tree contains .git")
	}

	dest = filepath.Join(t.TempDir(), "community")
	if err := Export(ctx, url, commits[1], dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "odoo-bin"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("odoo-bin mode = %v, %v", info, err)
	}
	if link, err := os.Readlink(filepath.Join(dest, "release-link")); err != nil || link != "odoo/release.py" {
		t.Fatalf("symlink = %q, %v", link, err)
	}
}

func TestExportErrors(t *testing.T) {
	url, _ := sourceRepo(t)
	ctx := context.Background()
	dest := func() string { return filepath.Join(t.TempDir(), "out") }
	cases := map[string]struct{ url, commit string }{
		"absent commit": {url, strings.Repeat("a", 40)},
		"short commit":  {url, "abc123"},
		"option url":    {"--upload-pack=touch /tmp/x", strings.Repeat("a", 40)},
		"missing repo":  {"file:///nonexistent/odooboat-repo", strings.Repeat("a", 40)},
	}
	for name, tc := range cases {
		if err := Export(ctx, tc.url, tc.commit, dest()); !errors.Is(err, ErrFetch) {
			t.Errorf("%s: err = %v, want ErrFetch", name, err)
		}
	}
}

func TestExtractRejectsEscapingEntries(t *testing.T) {
	for _, name := range []string{"../evil", "/abs"} {
		archive := tarWith(t, name)
		if err := extract(archive, t.TempDir()); !errors.Is(err, ErrFetch) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestExtractRejectsWritesThroughLinks(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: t.TempDir()})
	_ = tw.WriteHeader(&tar.Header{Name: "escape/planted", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if err := extract(&buf, t.TempDir()); !errors.Is(err, ErrFetch) {
		t.Fatalf("err = %v", err)
	}
}

func tarWith(t *testing.T, name string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	return &buf
}
