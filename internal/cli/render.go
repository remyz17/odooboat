package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/remyz17/odooboat/internal/config"
	"gopkg.in/yaml.v3"
)

func (g *globalFlags) format(cfg config.ResolvedConfig) (string, error) {
	return g.formatOf(cfg.Preferences)
}

func (g *globalFlags) formatOf(prefs config.ResolvedPreferences) (string, error) {
	if g.output == "" {
		return prefs.Output, nil
	}
	return g.checkedFormat()
}

func (g *globalFlags) formatOrDefault() (string, error) {
	if g.output == "" {
		return config.OutputYAML, nil
	}
	return g.checkedFormat()
}

func (g *globalFlags) checkedFormat() (string, error) {
	switch g.output {
	case config.OutputYAML, config.OutputJSON:
		return g.output, nil
	default:
		return "", fmt.Errorf("%w: unknown output format %q, expected yaml or json", ErrUsage, g.output)
	}
}

func render(w io.Writer, format string, value any) error {
	if format == config.OutputJSON {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return encoder.Close()
}
