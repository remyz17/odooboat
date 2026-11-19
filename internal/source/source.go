// Package source inspects Odoo source trees. It reads files and never runs
// Python on the host, so inspection works without a host interpreter and
// cannot execute code from the inspected tree (ADR 0008 §8).
//
// Acquisition from Git and presentation to a runtime are added with the
// environment image build; this package starts with local inspection.
package source

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

var (
	// ErrNotOdooSource: the directory does not contain the expected Odoo files.
	ErrNotOdooSource = errors.New("not an Odoo source tree")
	// ErrUnreadableSeries: the marker exists but its content is not understood.
	ErrUnreadableSeries = errors.New("odoo series could not be read")
	// ErrSeriesMismatch: a source does not have the configured series.
	ErrSeriesMismatch = errors.New("odoo source series does not match")
	// ErrOutsideTree: a file odooboat reads resolves, through a symbolic
	// link, outside the source tree. Embedded sources come from a repository
	// odooboat does not control; following such a link would copy a host
	// file into the build context.
	ErrOutsideTree = errors.New("source file leads outside the source tree")
)

const (
	releaseFile      = "odoo/release.py"
	requirementsFile = "requirements.txt"
	// enterpriseMarker is the translation template of the enterprise web
	// client. Odoo regenerates it for every series, and its header names the
	// series (see ReadEnterpriseSeries).
	enterpriseMarker = "web_enterprise/i18n/web_enterprise.pot"
	// enterpriseHeaderLimit bounds the bytes read: the header precedes every
	// translation entry.
	enterpriseHeaderLimit = 4096
)

// versionInfoRe matches the assignment Odoo uses in odoo/release.py, such as
//
//	version_info = (18, 0, 0, FINAL, 0, '')
//	version_info = ('saas~18', 1, 0, FINAL, 0, '')
//
// Only the first two members form the series, as in Odoo's own
// '.'.join(str(s) for s in version_info[:2]). The match is anchored at the
// start of a line so comments mentioning version tuples are ignored.
var versionInfoRe = regexp.MustCompile(`(?m)^version_info[ \t]*=[ \t]*\([ \t]*(\d+|'saas~\d+'|"saas~\d+")[ \t]*,[ \t]*(\d+)[ \t]*,`)

// ReadSeries returns the series of a community source tree, such as "18.0" or
// "saas~18.1", read statically from odoo/release.py.
func ReadSeries(dir string) (string, error) {
	path, data, err := readInTree(dir, releaseFile, 0)
	if err != nil {
		return "", err
	}
	matches := versionInfoRe.FindAllSubmatch(data, -1)
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: no version_info assignment in %s", ErrUnreadableSeries, path)
	case 1:
	default:
		// Two assignments would make the static reading ambiguous.
		return "", fmt.Errorf("%w: several version_info assignments in %s", ErrUnreadableSeries, path)
	}
	major := string(matches[0][1])
	if major[0] == '\'' || major[0] == '"' {
		major = major[1 : len(major)-1]
	}
	return major + "." + string(matches[0][2]), nil
}

// projectIDRe matches the template header written by Odoo's translation
// export, such as
//
//	"Project-Id-Version: Odoo Server 18.0+e\n"
//	"Project-Id-Version: Odoo Server saas~18.1+e\n"
//
// The "+e" suffix marks the enterprise edition.
var projectIDRe = regexp.MustCompile(`(?m)^"Project-Id-Version: Odoo Server ((?:saas~)?\d+\.\d+)\+e\\n"$`)

// ReadEnterpriseSeries returns the series of an enterprise source tree.
// Enterprise has no release.py and its manifests do not carry the series, so
// the series is read from the header of the web_enterprise translation
// template. Other modules' templates may keep the header of the series they
// were last exported in; web_enterprise's is regenerated with every series.
func ReadEnterpriseSeries(dir string) (string, error) {
	path, header, err := readInTree(dir, enterpriseMarker, enterpriseHeaderLimit)
	if err != nil {
		return "", err
	}
	match := projectIDRe.FindSubmatch(header)
	if match == nil {
		return "", fmt.Errorf("%w: no enterprise Project-Id-Version header in %s", ErrUnreadableSeries, path)
	}
	return string(match[1]), nil
}

// ReadRequirements returns the content of the tree's requirements.txt. The
// environment image depends on it, not on the source code (ADR 0008 §11).
func ReadRequirements(dir string) ([]byte, error) {
	_, data, err := readInTree(dir, requirementsFile, 0)
	return data, err
}

// readInTree reads the regular file name below dir, at most limit bytes when
// limit is positive. Resolution stays inside the tree: a symbolic link may
// lead elsewhere in it, never out of it. Links the tree contains but odooboat
// does not read are left alone; they only matter inside the container.
func readInTree(dir, name string, limit int64) (string, []byte, error) {
	path := filepath.Join(dir, filepath.FromSlash(name))
	missing := func() (string, []byte, error) {
		return path, nil, fmt.Errorf("%w: %s is missing", ErrNotOdooSource, path)
	}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil, fmt.Errorf("%w: directory %s does not exist", ErrNotOdooSource, dir)
	}
	if err != nil {
		return path, nil, err
	}
	defer root.Close()

	file, err := root.Open(filepath.FromSlash(name))
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return missing()
	case errors.Is(err, fs.ErrPermission):
		return path, nil, err
	default:
		// The file exists when links are followed freely, yet the root
		// refused it: a link on the way leaves the tree.
		if _, statErr := os.Stat(path); statErr == nil {
			return path, nil, fmt.Errorf("%w: %s", ErrOutsideTree, path)
		}
		return path, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return path, nil, err
	}
	if !info.Mode().IsRegular() {
		return path, nil, fmt.Errorf("%w: %s is not a regular file", ErrNotOdooSource, path)
	}
	var reader io.Reader = file
	if limit > 0 {
		reader = io.LimitReader(file, limit)
	}
	data, err := io.ReadAll(reader)
	return path, data, err
}
