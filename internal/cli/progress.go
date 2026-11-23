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

	stopTicks chan struct{}
	ticks     sync.WaitGroup
	closeOnce sync.Once
}

func newPresenter(stderr io.Writer, format ui.Format, retention ui.Retention, now func() time.Time) *presenter {
	out := ui.NewWriter(stderr)
	sink := ui.NewPlain(out)
	if format == ui.FormatJSON {
		sink = ui.NewJSON(out)
	}
	p := &presenter{out: out, queue: ui.NewQueue(ui.Retain(sink, retention), now), format: format, now: now,
		stopTicks: make(chan struct{})}
	if format == ui.FormatPlain {
		p.ticks.Add(1)
		go p.tick()
	}
	return p
}

// tick drives the plain heartbeat; the renderer decides when to print.
func (p *presenter) tick() {
	defer p.ticks.Done()
	ticker := time.NewTicker(time.Second)
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
// everything reported so far is written.
func (p *presenter) SetRaw(raw bool) {
	p.queue.Flush()
	p.out.SetRaw(raw)
}

func (p *presenter) Flush() { p.queue.Flush() }

// Fail writes err as the invocation's last record, then closes. usage is
// the command's usage text, printed only by plain progress.
func (p *presenter) Fail(err error, usage string) {
	p.queue.Post(ui.Record{Time: p.now(), Event: ui.ErrorReport{
		Code: ErrorCode(err), Message: err.Error(), Hint: app.Hint(err), Usage: usage,
	}})
	p.Close()
}

// Close writes everything reported, then stops.
func (p *presenter) Close() {
	p.closeOnce.Do(func() {
		close(p.stopTicks)
		p.ticks.Wait()
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
	progressFormats = []string{string(ui.FormatAuto), string(ui.FormatPlain), string(ui.FormatJSON)}
	retentionModes  = []string{string(ui.RetainOnFailure), string(ui.RetainAll)}
)

// presentation is what selects the presenter of an invocation.
type presentation struct {
	progress, stepOutput, output, userConfig string
}

// validate rejects unknown values with a usage error.
func (s presentation) validate(cmd *cobra.Command) error {
	if !slices.Contains(progressFormats, s.progress) {
		return usageErrorf(cmd, "unknown progress format %q, expected %s", s.progress, strings.Join(progressFormats, ", "))
	}
	if !slices.Contains(retentionModes, s.stepOutput) {
		return usageErrorf(cmd, "unknown step output mode %q, expected %s", s.stepOutput, strings.Join(retentionModes, " or "))
	}
	return nil
}

// presenter builds the presenter s selects. auto needs to know whether the
// result is json: --output says so, or else the user's preferences; a
// configuration error there means "not json", and the command reports it.
func (s presentation) presenter(deps Dependencies) *presenter {
	format, retention := ui.Format(s.progress), ui.Retention(s.stepOutput)
	if !slices.Contains(progressFormats, s.progress) {
		format = ui.FormatPlain
	}
	if !slices.Contains(retentionModes, s.stepOutput) {
		retention = ui.RetainOnFailure
	}
	if format == ui.FormatAuto {
		resultJSON := s.output == config.OutputJSON
		if s.output == "" {
			prefs, err := config.ResolvePreferences(config.Options{WorkingDir: deps.WorkingDir, UserConfigPath: s.userConfig})
			resultJSON = err == nil && prefs.Output == config.OutputJSON
		}
		live := ui.LiveRedraw(deps.terminal().StderrIsTerminal(), deps.getenv())
		format = ui.ResolveFormat(format, live, resultJSON)
	}
	return newPresenter(deps.Stderr, format, retention, deps.now())
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
