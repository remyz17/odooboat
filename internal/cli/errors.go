package cli

import (
	"context"
	"errors"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/config"
)

// errorClass gives a class of errors its stable code and exit status. One
// table serves both, so they cannot drift (ADR 0011 §9).
type errorClass struct {
	code      string
	exit      int
	sentinels []error
}

// errorClasses is searched in order; the first class an error matches wins.
var errorClasses = []errorClass{
	{"usage", ExitUsage, []error{ErrUsage}},
	{"config", ExitConfig, []error{config.ErrNotFound, config.ErrDecode, config.ErrAuthored, config.ErrResolved, app.ErrSourceRequired}},
	{"state", ExitState, []error{app.ErrState}},
	{"runtime-unavailable", ExitRuntime, []error{app.ErrRuntimeUnavailable}},
	{"runtime-unsupported", ExitRuntime, []error{app.ErrRuntimeUnsupported}},
	{"locked", ExitBusy, []error{app.ErrEnvironmentBusy}},
	{"not-found", ExitImage, []error{app.ErrImageNotFound}},
	{"invalid-image", ExitImage, []error{app.ErrInvalidImage}},
	{"image-build", ExitImage, []error{app.ErrImageBuild}},
	{"source", ExitSource, []error{app.ErrSourceMismatch, app.ErrSourceFetch, app.ErrSourceInvalid}},
	{"conflict", ExitConflict, []error{app.ErrConflict}},
	{"incompatible-data", ExitConflict, []error{app.ErrIncompatibleData}},
	{"not-ready", ExitNotReady, []error{app.ErrNotReady}},
	{"not-running", ExitNotRunning, []error{app.ErrNotRunning}},
	{"database", ExitError, []error{app.ErrDatabase}},
	{"termination-unconfirmed", ExitError, []error{app.ErrTerminationUnconfirmed}},
	{"interrupted", ExitError, []error{context.Canceled}},
}

func classOf(err error) (errorClass, bool) {
	for _, c := range errorClasses {
		for _, sentinel := range c.sentinels {
			if errors.Is(err, sentinel) {
				return c, true
			}
		}
	}
	return errorClass{}, false
}

// ExitCode is the process exit status for err.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if c, ok := classOf(err); ok {
		return c.exit
	}
	return ExitError
}

// ErrorCode is the stable error code for err, as JSON errors report it.
func ErrorCode(err error) string {
	if c, ok := classOf(err); ok {
		return c.code
	}
	return "error"
}
