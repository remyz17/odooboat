// Package ui renders progress, events, and errors for the CLI (ADR 0011).
// It is the only place that chooses wording for application events.
package ui

import "time"

// Record is an event stamped with the time it was reported. Event is an
// app.Event, or one of the CLI's own records: Tick, Warning, ErrorReport.
type Record struct {
	Time  time.Time
	Event any
}

// Tick is the heartbeat clock; renderers decide whether it prints anything.
type Tick struct{}

// Warning is a result warning echoed on stderr.
type Warning struct{ Message string }

// Sink handles records one at a time, in order.
type Sink interface{ Handle(Record) }

// Settle erases the live region of the tty renderer. Hold true stops it
// drawing until a Settle with Hold false: a session owns the terminal.
type Settle struct{ Hold bool }

// Finish ends the invocation: renderers write what they still hold.
type Finish struct{}
