// Package git acquires embedded Odoo sources (ADR 0008 §9). It runs the host's
// git so the user's credential helpers and SSH agent apply, and it exports the
// pinned commit as a plain tree: no .git directory and no credential ever
// reach the build context.
package git

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrFetch reports a repository or commit that could not be fetched.
var ErrFetch = errors.New("git source could not be fetched")

var commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Export writes the tree of commit from the repository at url into dest,
// which must not exist. The commit must be a full SHA: it is the identity of
// the embedded source.
func Export(ctx context.Context, url, commit, dest string) error {
	if !commitRe.MatchString(commit) {
		return fmt.Errorf("%w: commit %q must be a full 40-character SHA", ErrFetch, commit)
	}
	// A leading dash would be read as an option by git.
	if url == "" || strings.HasPrefix(url, "-") {
		return fmt.Errorf("%w: invalid repository URL", ErrFetch)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("%w: git is not installed on this host", ErrFetch)
	}
	repo, err := os.MkdirTemp("", "odooboat-git-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(repo)

	if _, err := run(ctx, repo, "init", "--quiet", "--bare"); err != nil {
		return err
	}
	// Fetching one commit by SHA is the cheap path; servers that refuse
	// unadvertised objects get a blobless fetch of their branches and tags.
	if _, err := run(ctx, repo, "fetch", "--quiet", "--no-tags", "--depth", "1", "--", url, commit); err != nil {
		if _, fallback := run(ctx, repo, "fetch", "--quiet", "--filter=blob:none", "--", url,
			"+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*"); fallback != nil {
			return err
		}
	}
	if _, err := run(ctx, repo, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return fmt.Errorf("%w: commit %s is not in the repository", ErrFetch, commit)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		return err
	}
	return archive(ctx, repo, commit, dest)
}

// archive streams git archive into extract: an Odoo tree is hundreds of
// megabytes and is never held in memory.
func archive(ctx context.Context, repo, commit, dest string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, stderr := command(ctx, repo, "archive", "--format=tar", commit)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return failure(ctx, "archive", err, stderr)
	}
	extractErr := extract(stdout, dest)
	if extractErr != nil {
		// Stop git rather than read an archive that is already rejected.
		cancel()
	} else {
		// The tar end marker may be followed by record padding; reading it
		// lets git exit instead of blocking on a full pipe.
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		return failure(ctx, "archive", waitErr, stderr)
	}
	return nil
}

// run executes git and returns its output; failures carry git's own message.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd, stderr := command(ctx, dir, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, failure(ctx, args[0], err, stderr)
	}
	return stdout.Bytes(), nil
}

// command prepares git without a terminal: a missing credential fails
// instead of prompting.
func command(ctx context.Context, dir string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd, &stderr
}

func failure(ctx context.Context, verb string, err error, stderr *bytes.Buffer) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("%w: git %s: %s", ErrFetch, verb, message)
}

// extract unpacks a git archive. Entries escaping dest are rejected; symbolic
// links are recreated as links, since Odoo trees contain a few, and no entry
// may be written through one.
func extract(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	var links []string
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: read archive: %v", ErrFetch, err)
		}
		name := filepath.Clean(header.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: archive entry %q escapes the tree", ErrFetch, header.Name)
		}
		for _, link := range links {
			if strings.HasPrefix(name, link+string(filepath.Separator)) {
				return fmt.Errorf("%w: archive entry %q is below the symbolic link %q", ErrFetch, header.Name, link)
			}
		}
		target := filepath.Join(dest, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tr)
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
			links = append(links, name)
		case tar.TypeXGlobalHeader:
			// git archive records the commit in a pax global header.
		default:
			return fmt.Errorf("%w: archive entry %q has unsupported type %q", ErrFetch, header.Name, header.Typeflag)
		}
	}
}
