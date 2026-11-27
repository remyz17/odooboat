package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

// Wording shared by the progress renderers, so plain and tty cannot drift.

// shortRef shortens the digest of an image reference, and a tag that is a
// full hash, to 12 characters.
func shortRef(ref string) string {
	return hashTag.ReplaceAllString(digest.ReplaceAllString(ref, "@sha256:$1"), ":$1")
}

// hiddenKind reports whether steps on this kind of resource are shown only
// when they fail: layers and build steps are detail of their parent.
func hiddenKind(k app.ResourceKind) bool {
	return k == app.ResourceLayer || k == app.ResourceBuildStep
}

// stepLabel words a step: "pull image postgres:16 (postgres)".
func stepLabel(s app.StepStarted, withRole bool) string {
	sub := s.Subject
	label := fmt.Sprintf("%s %s %s", s.Kind, sub.Kind, shortRef(sub.Name))
	if withRole && sub.Role != "" {
		label += " (" + string(sub.Role) + ")"
	}
	return label
}

// messageText words a record that is not part of a step, each line ending
// in a newline; ok is false for any other record.
func messageText(event any) (text string, ok bool) {
	switch e := event.(type) {
	case app.LockWaiting:
		if e.Holder == nil {
			return "waiting for the environment lock held by an unknown process\n", true
		}
		return fmt.Sprintf("waiting for the environment lock held by %q (pid %d on %s since %s)\n",
			e.Holder.Operation, e.Holder.PID, e.Holder.Host, e.Holder.StartedAt.In(zone).Format(time.TimeOnly)), true
	case app.DatabaseSelected:
		return fmt.Sprintf("using database %q\n", e.Name), true
	case app.SessionDegraded:
		return fmt.Sprintf("warning: %s failed: %v\n", e.Operation, e.Err), true
	case Warning:
		return fmt.Sprintf("warning: %s\n", e.Message), true
	case ErrorReport:
		var b strings.Builder
		b.WriteString(e.Usage)
		fmt.Fprintf(&b, "error: %s\n", e.Message)
		if e.Hint != "" {
			fmt.Fprintf(&b, "hint: %s\n", e.Hint)
		}
		return b.String(), true
	}
	return "", false
}
