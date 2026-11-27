package cli

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/spf13/cobra"
)

// presenter owns stderr for one invocation: events, warnings, and the final
// error go through it, in one format (ADR 0011 §7, §9).
type presenter struct {
	out    *ui.Writer
	queue  *ui.Queue
	format ui.Format
	now    func() time.Time
	raw    bool

	stopTicks chan struct{}
	ticks     sync.WaitGroup
	closeOnce sync.Once
}

// newPresenter builds the presenter of one invocation. view is used by tty
// progress only.
func newPresenter(stderr io.Writer, format ui.Format, retention ui.Retention, now func() time.Time, view ui.View) *presenter {
	out := ui.NewWriter(stderr)
	var sink ui.Sink
	switch format {
	case ui.FormatTTY:
		// The live renderer applies retention itself: it shows output as it
		// arrives (ADR 0011 §7).
		sink = ui.NewTTY(out, retention, view)
	case ui.FormatJSON:
		sink = ui.Retain(ui.NewJSON(out), retention)
	default:
		sink = ui.Retain(ui.NewPlain(out), retention)
	}
	p := &presenter{out: out, queue: ui.NewQueue(sink, now), format: format, now: now,
		stopTicks: make(chan struct{})}
	switch format {
	case ui.FormatPlain:
		p.ticks.Add(1)
		go p.tick(time.Second)
	case ui.FormatTTY:
		p.ticks.Add(1)
		go p.tick(ui.TTYRefresh)
	}
	return p
}

// tick drives the plain heartbeat and the tty redraws; the renderer
// decides what to print.
func (p *presenter) tick(every time.Duration) {
	defer p.ticks.Done()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.queue.Post(ui.Record{Time: p.now(), Event: ui.Tick{}})
		case <-p.stopTicks:
			return
		}
	}
}

// Reporter is where the application reports this invocation's events.
func (p *presenter) Reporter() app.Reporter { return p.queue }

// Warn echoes result warnings on stderr; json progress leaves them in the
// result only.
func (p *presenter) Warn(messages []string) {
	for _, m := range messages {
		p.queue.Post(ui.Record{Time: p.now(), Event: ui.Warning{Message: m}})
	}
}

// SetRaw switches newline translation for a terminal in raw mode, after
// everything reported so far is written. While raw, a session owns the
// terminal and live progress is not drawn.
func (p *presenter) SetRaw(raw bool) {
	p.raw = raw
	p.queue.Post(ui.Record{Time: p.now(), Event: ui.Settle{Hold: raw}})
	p.queue.Flush()
	p.out.SetRaw(raw)
}

// Flush returns once everything reported is written and live progress is
// erased, so stdout can be written below it.
func (p *presenter) Flush() {
	p.queue.Post(ui.Record{Time: p.now(), Event: ui.Settle{Hold: p.raw}})
	p.queue.Flush()
}

// Fail writes err as the invocation's last record, then closes. usage is
// the command's usage text, printed only by plain progress.
func (p *presenter) Fail(err error, usage string) {
	message := err.Error()
	if message == context.Canceled.Error() {
		// A bare cancellation is the user's interrupt, not a failure to explain.
		message = "interrupted"
	}
	p.queue.Post(ui.Record{Time: p.now(), Event: ui.ErrorReport{
		Code: ErrorCode(err), Message: message, Hint: app.Hint(err), Usage: usage,
	}})
	p.Close()
}

// Close writes everything reported, then stops.
func (p *presenter) Close() {
	p.closeOnce.Do(func() {
		close(p.stopTicks)
		p.ticks.Wait()
		p.queue.Post(ui.Record{Time: p.now(), Event: ui.Finish{}})
		p.queue.Close()
	})
}

type presenterKey struct{}

// presenterOf returns the presenter set up for cmd's invocation, or nil when
// none was (help, version, or a failure before the command ran).
func presenterOf(cmd *cobra.Command) *presenter {
	if cmd == nil || cmd.Context() == nil {
		return nil
	}
	p, _ := cmd.Context().Value(presenterKey{}).(*presenter)
	return p
}

var (
	resultFormats   = []string{config.OutputText, config.OutputYAML, config.OutputJSON}
	progressFormats = []string{string(ui.FormatAuto), string(ui.FormatTTY), string(ui.FormatPlain), string(ui.FormatJSON)}
	retentionModes  = []string{string(ui.RetainOnFailure), string(ui.RetainAll)}
)

// presentation is what selects the presenter of an invocation.
type presentation struct {
	progress, stepOutput, output, userConfig string
	noColor                                  bool
}

// validate rejects unknown values with a usage error.
func (s presentation) validate(cmd *cobra.Command) error {
	if s.output != "" && !slices.Contains(resultFormats, s.output) {
		return usageErrorf(cmd, "unknown output format %q, expected %s", s.output, "text, yaml, or json")
	}
	if !slices.Contains(progressFormats, s.progress) {
		return usageErrorf(cmd, "unknown progress format %q, expected %s", s.progress, strings.Join(progressFormats, ", "))
	}
	if !slices.Contains(retentionModes, s.stepOutput) {
		return usageErrorf(cmd, "unknown step output mode %q, expected %s", s.stepOutput, strings.Join(retentionModes, " or "))
	}
	return nil
}

// result resolves the result format and the colour preference: --output,
// else the user's preferences, else text. A configuration error there means
// the defaults; the command reports the error if it reads the configuration.
func (s presentation) result(deps Dependencies) (format string, color bool) {
	format, color = config.OutputText, true
	if prefs, err := config.ResolvePreferences(config.Options{WorkingDir: deps.WorkingDir, UserConfigPath: s.userConfig}); err == nil {
		format, color = prefs.Output, prefs.Color
	}
	if s.output != "" {
		format = s.output
	}
	return format, color
}

// presenter builds the presenter s selects. auto needs to know whether the
// result is json.
func (s presentation) presenter(deps Dependencies) *presenter {
	format, retention := ui.Format(s.progress), ui.Retention(s.stepOutput)
	if !slices.Contains(progressFormats, s.progress) {
		format = ui.FormatPlain
	}
	if !slices.Contains(retentionModes, s.stepOutput) {
		retention = ui.RetainOnFailure
	}
	terminal, getenv := deps.terminal(), deps.getenv()
	resultFormat, color := s.result(deps)
	if format == ui.FormatAuto {
		live := ui.LiveRedraw(terminal.StderrIsTerminal(), getenv)
		format = ui.ResolveFormat(format, live, resultFormat == config.OutputJSON)
		// Live redrawing also needs the width, or wrapped lines break the
		// cursor arithmetic; a person at that terminal still gets text.
		if width, _ := terminal.StderrSize(); format == ui.FormatTTY && width <= 0 {
			format = ui.FormatPlain
		}
	}
	view := ui.View{Caps: ui.DetectCapabilities(terminal.StderrIsTerminal(), getenv, color, s.noColor), Size: terminal.StderrSize}
	return newPresenter(deps.Stderr, format, retention, deps.now(), view)
}

// scanPresentation reads the presentation flags from raw arguments, for a
// failure that happened before the flags were parsed.
func scanPresentation(args []string) presentation {
	s := presentation{progress: string(ui.FormatAuto), stepOutput: string(ui.RetainOnFailure)}
	targets := map[string]*string{
		"--progress": &s.progress, "--step-output": &s.stepOutput,
		"--output": &s.output, "-o": &s.output, "--user-config": &s.userConfig,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if name, value, ok := strings.Cut(arg, "="); ok {
			if target, known := targets[name]; known {
				*target = value
			}
			continue
		}
		if target, known := targets[arg]; known && i+1 < len(args) {
			*target = args[i+1]
			i++
			continue
		}
		if arg == "--no-color" {
			s.noColor = true
			continue
		}
		if strings.HasPrefix(arg, "-o") && len(arg) > 2 && !strings.HasPrefix(arg, "--") {
			s.output = arg[2:]
		}
	}
	return s
}

// withPresenter returns ctx carrying p, for presenterOf.
func withPresenter(ctx context.Context, p *presenter) context.Context {
	return context.WithValue(ctx, presenterKey{}, p)
}
