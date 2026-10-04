package runtime

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/remyz17/odooboat/internal/workspace"
)

// MaxNameLength keeps names usable as DNS labels on the environment network.
const MaxNameLength = 63

const namePrefix = "ob"

var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ResourceName returns ob-<project>-<environment>-<role>-<env8>. Names give
// uniqueness and readability only; they never establish identity (ADR 0005 §5).
func ResourceName(project, environment string, role Role, environmentID string) (string, error) {
	return buildName(project, environment, role, environmentID, "")
}

// JobName appends a segment derived from the operation UUID so ephemeral jobs
// of the same environment do not collide.
func JobName(project, environment, environmentID, operationID string) (string, error) {
	if !workspace.ValidUUID(operationID) {
		return "", fmt.Errorf("job name: operation ID %q is not a UUID", operationID)
	}
	return buildName(project, environment, RoleJob, environmentID, operationID[:8])
}

func buildName(project, environment string, role Role, environmentID, suffix string) (string, error) {
	if !workspace.ValidUUID(environmentID) {
		return "", fmt.Errorf("resource name: environment ID %q is not a UUID", environmentID)
	}
	if !dnsLabelRe.MatchString(string(role)) {
		return "", fmt.Errorf("resource name: role %q is not DNS-safe", role)
	}
	p, e := sanitizeSegment(project), sanitizeSegment(environment)
	if p == "" || e == "" {
		return "", fmt.Errorf("resource name: project %q and environment %q must contain a letter or digit", project, environment)
	}
	tail := []string{string(role), environmentID[:8]}
	if suffix != "" {
		tail = append(tail, suffix)
	}
	fixed := len(namePrefix) + 1 + 1 + 1 + len(strings.Join(tail, "-"))
	p, e = fitSegments(p, e, MaxNameLength-fixed)
	p, e = strings.TrimRight(p, "-"), strings.TrimRight(e, "-")
	name := strings.Join(append([]string{namePrefix, p, e}, tail...), "-")
	if len(name) > MaxNameLength || !dnsLabelRe.MatchString(name) {
		return "", fmt.Errorf("resource name: %q is not a valid DNS label", name)
	}
	return name, nil
}

// sanitizeSegment lowercases and maps every character outside [a-z0-9] to a
// single dash, trimming dashes at both ends.
func sanitizeSegment(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// fitSegments shortens the sanitized project and environment segments so that
// len(project)+len(environment) <= budget. Project has priority: environment
// is shortened first, and project is shortened only when necessary to preserve
// at least three environment characters. Both results remain non-empty prefixes
// of their inputs (trailing dashes are trimmed by the caller).
func fitSegments(project, environment string, budget int) (string, string) {
	if len(project)+len(environment) <= budget {
		return project, environment
	}

	projectLen := min(len(project), budget-3)
	environmentLen := min(len(environment), budget-projectLen)

	return project[:projectLen], environment[:environmentLen]
}
