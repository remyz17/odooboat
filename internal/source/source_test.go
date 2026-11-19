package source

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures are odoo/release.py files copied from upstream branches.
func TestReadSeriesFromUpstreamReleases(t *testing.T) {
	for dir, want := range map[string]string{
		"community-16.0":      "16.0",
		"community-17.0":      "17.0",
		"community-18.0":      "18.0",
		"community-19.0":      "19.0",
		"community-20.0":      "20.0",
		"community-saas-18.1": "saas~18.1",
	} {
		got, err := ReadSeries(filepath.Join("testdata", dir))
		if err != nil || got != want {
			t.Errorf("%s: series = %q, %v; want %q", dir, got, err, want)
		}
	}
}

func writeRelease(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "odoo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, releaseFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadSeriesFormatting(t *testing.T) {
	cases := map[string]string{
		"spaces":         "version_info = ( 17 ,  0 , 0, FINAL, 0, '')\n",
		"no spaces":      "version_info=(17,0,0,FINAL,0,'')\n",
		"double quotes":  "version_info = (\"saas~17\", 0, 0, FINAL, 0, '')\n",
		"after comments": "# version_info = (6, 1, 0, 'beta', 0)\n#  (6,1,0,'beta',0) < (6,1,0,'final',0)\nversion_info = (17, 0, 0, FINAL, 0, '')\n",
	}
	want := map[string]string{"double quotes": "saas~17.0"}
	for name, content := range cases {
		expected := want[name]
		if expected == "" {
			expected = "17.0"
		}
		got, err := ReadSeries(writeRelease(t, content))
		if err != nil || got != expected {
			t.Errorf("%s: series = %q, %v; want %q", name, got, err, expected)
		}
	}
}

func TestReadSeriesErrors(t *testing.T) {
	if _, err := ReadSeries(t.TempDir()); !errors.Is(err, ErrNotOdooSource) {
		t.Errorf("missing release.py: err = %v", err)
	}
	unreadable := map[string]string{
		"no assignment":      "series = '17.0'\n",
		"indented":           "if True:\n    version_info = (17, 0, 0, FINAL, 0, '')\n",
		"computed major":     "version_info = (MAJOR, 0, 0, FINAL, 0, '')\n",
		"several assignment": "version_info = (17, 0, 0, FINAL, 0, '')\nversion_info = (18, 0, 0, FINAL, 0, '')\n",
	}
	for name, content := range unreadable {
		if _, err := ReadSeries(writeRelease(t, content)); !errors.Is(err, ErrUnreadableSeries) {
			t.Errorf("%s: err = %v, want ErrUnreadableSeries", name, err)
		}
	}
}

func TestReadRequirements(t *testing.T) {
	data, err := ReadRequirements(filepath.Join("testdata", "community-18.0"))
	if err != nil || len(data) == 0 {
		t.Fatalf("requirements = %d bytes, %v", len(data), err)
	}
	if _, err := ReadRequirements(t.TempDir()); !errors.Is(err, ErrNotOdooSource) {
		t.Fatalf("missing requirements: err = %v", err)
	}
}

// The fixtures are web_enterprise.pot headers copied from upstream branches.
func TestReadEnterpriseSeriesFromUpstreamTemplates(t *testing.T) {
	for dir, want := range map[string]string{
		"enterprise-16.0":      "16.0",
		"enterprise-17.0":      "17.0",
		"enterprise-18.0":      "18.0",
		"enterprise-19.0":      "19.0",
		"enterprise-20.0":      "20.0",
		"enterprise-saas-18.1": "saas~18.1",
	} {
		got, err := ReadEnterpriseSeries(filepath.Join("testdata", dir))
		if err != nil || got != want {
			t.Errorf("%s: series = %q, %v; want %q", dir, got, err, want)
		}
	}
}

func writeTemplate(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, enterpriseMarker)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadEnterpriseSeriesErrors(t *testing.T) {
	if _, err := ReadEnterpriseSeries(t.TempDir()); !errors.Is(err, ErrNotOdooSource) {
		t.Errorf("missing template: err = %v", err)
	}
	// A community tree is not an enterprise tree.
	if _, err := ReadEnterpriseSeries(filepath.Join("testdata", "community-18.0")); !errors.Is(err, ErrNotOdooSource) {
		t.Errorf("community tree: err = %v", err)
	}
	unreadable := map[string]string{
		"empty":            "",
		"community header": "msgid \"\"\nmsgstr \"\"\n\"Project-Id-Version: Odoo Server 18.0\\n\"\n",
		"no version":       "msgid \"\"\nmsgstr \"\"\n\"Project-Id-Version: Odoo Server +e\\n\"\n",
		"beyond header":    "msgid \"\"\n" + strings.Repeat("#\n", enterpriseHeaderLimit) + "\"Project-Id-Version: Odoo Server 18.0+e\\n\"\n",
	}
	for name, content := range unreadable {
		if _, err := ReadEnterpriseSeries(writeTemplate(t, content)); !errors.Is(err, ErrUnreadableSeries) {
			t.Errorf("%s: err = %v, want ErrUnreadableSeries", name, err)
		}
	}
}

// Files odooboat reads may be links inside the tree, never out of it; other
// links of the tree are not its concern.
func TestReadsStayInsideTheTree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("private key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := writeRelease(t, "version_info = (18, 0, 0, FINAL, 0, '')\n")
	link := func(target, name string) {
		t.Helper()
		path := filepath.Join(tree, name)
		_ = os.Remove(path)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}

	link(outside, "requirements.txt")
	if data, err := ReadRequirements(tree); !errors.Is(err, ErrOutsideTree) || data != nil {
		t.Fatalf("absolute link out: %q, %v", data, err)
	}
	relative, err := filepath.Rel(tree, outside)
	if err != nil {
		t.Fatal(err)
	}
	link(relative, "requirements.txt")
	if _, err := ReadRequirements(tree); !errors.Is(err, ErrOutsideTree) {
		t.Fatalf("relative link out: %v", err)
	}
	// A whole directory leading out is refused as well.
	if err := os.Rename(filepath.Join(tree, "odoo"), filepath.Join(t.TempDir(), "odoo")); err != nil {
		t.Fatal(err)
	}
	link(filepath.Dir(outside), "odoo")
	if err := os.WriteFile(filepath.Join(filepath.Dir(outside), "release.py"), []byte("version_info = (18, 0, 0, FINAL, 0, '')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSeries(tree); !errors.Is(err, ErrOutsideTree) {
		t.Fatalf("directory link out: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tree, "pinned.txt"), []byte("lxml==5.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link("pinned.txt", "requirements.txt")
	link("../elsewhere", "unrelated-link")
	if data, err := ReadRequirements(tree); err != nil || string(data) != "lxml==5.2.1\n" {
		t.Fatalf("link inside the tree: %q, %v", data, err)
	}

	if err := os.Remove(filepath.Join(tree, "requirements.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tree, "requirements.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRequirements(tree); !errors.Is(err, ErrNotOdooSource) {
		t.Fatalf("directory as requirements.txt: %v", err)
	}
}

func TestReadsReportAnAbsentDirectory(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")
	if _, err := ReadSeries(absent); !errors.Is(err, ErrNotOdooSource) || !strings.Contains(err.Error(), "directory "+absent+" does not exist") {
		t.Fatalf("err = %v", err)
	}
}
