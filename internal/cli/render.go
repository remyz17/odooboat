package cli

import (
	"encoding/json"
	"io"

	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/config"
	"gopkg.in/yaml.v3"
)

// textRenderer is implemented by the CLI presentation types of results that
// have a text rendering (ADR 0011 §8). Application result types never
// implement it.
type textRenderer interface{ RenderText(p *ui.Printer) error }

// print writes value on stdout in the invocation's result format. text nil
// means the result has no text rendering yet: text falls back to yaml.
// warnings are echoed on stderr unless the text rendering shows them.
func (g *globalFlags) print(deps Dependencies, value any, text textRenderer, warnings []string) error {
	if g.resultFormat == config.OutputText && text != nil {
		getenv := deps.getenv()
		caps := ui.DetectCapabilities(deps.terminal().StdoutIsTerminal(), getenv, g.colorPreference, g.noColor)
		return text.RenderText(ui.NewPrinter(deps.Stdout, caps, getenv("HOME")))
	}
	g.warn(warnings)
	return marshal(deps.Stdout, g.resultFormat, value)
}

func marshal(w io.Writer, format string, value any) error {
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
