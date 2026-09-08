package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/config"
)

func fixtureSelector(t *testing.T, env string) Selector {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "config", "valid"))
	if err != nil {
		t.Fatalf("locate fixtures: %v", err)
	}
	return Selector{
		WorkingDir:     filepath.Join(root, "project"),
		UserConfigPath: filepath.Join(root, "user.yaml"),
		Environment:    env,
	}
}

func TestValidate(t *testing.T) {
	result, err := NewConfigService().Validate(context.Background(), ValidateRequest{Selector: fixtureSelector(t, "dev")})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.Sources.EnvironmentName != "dev" {
		t.Errorf("environment = %q, want dev", result.Sources.EnvironmentName)
	}
	if result.Config.OdooVersion != "17.0" {
		t.Errorf("odooVersion = %q, want 17.0", result.Config.OdooVersion)
	}
}

func TestExplain(t *testing.T) {
	service := NewConfigService()

	t.Run("reports the winning source", func(t *testing.T) {
		result, err := service.Explain(context.Background(), ExplainRequest{
			Selector: fixtureSelector(t, "dev"),
			Field:    config.FieldOdooVersion,
		})
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if result.Value != "17.0" {
			t.Errorf("value = %q, want 17.0", result.Value)
		}
		if result.Winner.Kind != config.SourceEnvironment {
			t.Errorf("winner = %q, want environment", result.Winner.Kind)
		}
		if len(result.Overridden) != 1 {
			t.Errorf("overridden = %+v, want one entry", result.Overridden)
		}
	})

	t.Run("reports secret metadata only", func(t *testing.T) {
		result, err := service.Explain(context.Background(), ExplainRequest{
			Selector: fixtureSelector(t, ""),
			Field:    config.FieldOdooMasterPassword,
		})
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if !result.Secret {
			t.Error("the master password should be marked secret")
		}
		if result.Value != "env:ODOOBOAT_MASTER_PASSWORD" {
			t.Errorf("value = %q, want the env reference", result.Value)
		}
	})

	t.Run("rejects an unknown field", func(t *testing.T) {
		_, err := service.Explain(context.Background(), ExplainRequest{
			Selector: fixtureSelector(t, ""),
			Field:    "nope",
		})
		if err == nil {
			t.Fatal("expected an error, got none")
		}
		if !errors.Is(err, config.ErrResolved) {
			t.Errorf("error %v is not ErrResolved", err)
		}
		if !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("error %q does not mention an unknown field", err)
		}
	})
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	service := NewConfigService()

	result, err := service.Init(context.Background(), InitRequest{Dir: dir, ProjectName: "demo"})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if result.Path != filepath.Join(dir, config.ProjectFileName) {
		t.Errorf("path = %q, want a %s in %s", result.Path, config.ProjectFileName, dir)
	}

	original, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if !strings.Contains(string(original), "name: demo") {
		t.Errorf("generated document does not use the requested name:\n%s", original)
	}

	if _, err := service.Init(context.Background(), InitRequest{Dir: dir, ProjectName: "other"}); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("second init error = %v, want ErrProjectExists", err)
	}
	current, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("re-read generated file: %v", err)
	}
	if string(current) != string(original) {
		t.Error("the refused init modified the existing file")
	}
}
