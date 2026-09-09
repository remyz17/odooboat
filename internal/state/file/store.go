package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/remyz17/odooboat/internal/state"
	"github.com/remyz17/odooboat/internal/workspace"
)

type Store struct{}

var _ state.Store = Store{}

func New() Store { return Store{} }

func (Store) Load(path string) (workspace.State, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workspace.State{}, false, nil
	}
	if err != nil {
		return workspace.State{}, false, fmt.Errorf("%w: read %s: %v", workspace.ErrState, path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value workspace.State
	if err := decoder.Decode(&value); err != nil {
		return workspace.State{}, false, fmt.Errorf("%w: decode %s: %v", workspace.ErrCorrupt, path, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return workspace.State{}, false, fmt.Errorf("%w: decode %s: %v", workspace.ErrCorrupt, path, err)
	}
	if err := workspace.Validate(value); err != nil {
		return workspace.State{}, false, fmt.Errorf("%w: validate %s: %v", workspace.ErrCorrupt, path, err)
	}
	return value, true, nil
}

func (s Store) Mutate(location workspace.Location, fn func(workspace.State, bool) (workspace.State, bool, error)) (workspace.State, error) {
	dir := filepath.Dir(location.StateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return workspace.State{}, fmt.Errorf("%w: create state directory: %v", workspace.ErrState, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return workspace.State{}, fmt.Errorf("%w: protect state directory: %v", workspace.ErrState, err)
	}
	lock, err := acquireLock(location.StateFile + ".lock")
	if err != nil {
		return workspace.State{}, fmt.Errorf("%w: acquire state lock: %v", workspace.ErrState, err)
	}
	defer lock.close()
	if !location.Git {
		if err := ensureIgnore(dir); err != nil {
			return workspace.State{}, err
		}
	}

	current, exists, err := s.Load(location.StateFile)
	if err != nil {
		return workspace.State{}, err
	}
	next, changed, err := fn(current, exists)
	if err != nil {
		return workspace.State{}, err
	}
	if !changed {
		return next, nil
	}
	if err := workspace.Validate(next); err != nil {
		return workspace.State{}, err
	}
	if err := writeAtomic(location.StateFile, next); err != nil {
		return workspace.State{}, err
	}
	return next, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}

func ensureIgnore(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	const content = "*\n!.gitignore\n"
	data, err := os.ReadFile(path)
	switch {
	case err == nil && string(data) == content:
		return nil
	case err == nil:
		return fmt.Errorf("%w: refusing to replace existing %s", workspace.ErrState, path)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: inspect %s: %v", workspace.ErrState, path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("%w: create %s: %v", workspace.ErrState, path, err)
	}
	return nil
}

func writeAtomic(path string, value workspace.State) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode state: %v", workspace.ErrState, err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create temporary state: %v", workspace.ErrState, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: protect temporary state: %v", workspace.ErrState, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: write temporary state: %v", workspace.ErrState, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: sync temporary state: %v", workspace.ErrState, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close temporary state: %v", workspace.ErrState, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("%w: replace state: %v", workspace.ErrState, err)
	}
	d, err := os.Open(dir)
	if err == nil {
		defer d.Close()
		if err := d.Sync(); err != nil {
			return fmt.Errorf("%w: sync state directory: %v", workspace.ErrState, err)
		}
	}
	return nil
}
