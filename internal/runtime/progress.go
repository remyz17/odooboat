package runtime

// ProgressKind names what a progress report is about.
type ProgressKind string

const (
	// ProgressLayer is an image layer being pulled.
	ProgressLayer ProgressKind = "layer"
	// ProgressVertex is a step of an image build.
	ProgressVertex ProgressKind = "vertex"
)

// ProgressState is where a reported item stands.
type ProgressState string

const (
	ProgressStarted ProgressState = "started"
	ProgressCached  ProgressState = "cached"
	ProgressRunning ProgressState = "running"
	ProgressDone    ProgressState = "done"
	ProgressFailed  ProgressState = "failed"
)

// Terminal reports whether no report may follow s for the same ID.
func (s ProgressState) Terminal() bool {
	return s == ProgressCached || s == ProgressDone || s == ProgressFailed
}

// Progress is a neutral report of a pull or a build, decoded by the adapter
// from its engine's stream (ADR 0011 §5). It carries no wording: the layers
// above choose how to present it.
//
// For each non-empty ID, the first report has State started, exactly one
// terminal report (cached, done, or failed) follows, and nothing comes after
// it. Before the operation returns, every open ID is closed: failed when the
// operation fails or is cancelled, done otherwise. A report with an empty ID
// carries Output for the operation as a whole.
type Progress struct {
	ID, Parent string
	Kind       ProgressKind
	// Name is the layer ID or the build step's name.
	Name  string
	State ProgressState
	// Current and Total are bytes for layers; zero when unknown.
	Current, Total int64
	// Output is raw build output, not split into lines.
	Output []byte
}

// ProgressFunc receives progress reports synchronously; a nil func discards
// them. It must return promptly.
type ProgressFunc func(Progress)

// Report calls f with p, unless f is nil.
func (f ProgressFunc) Report(p Progress) {
	if f != nil {
		f(p)
	}
}
