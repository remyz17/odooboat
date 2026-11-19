package runtime

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/remyz17/odooboat/internal/workspace"
)

// OwnershipSchema is the label schema written on every resource Odooboat creates.
const OwnershipSchema = 1

// LabelPrefix is reserved in its entirety for Odooboat ownership labels (ADR 0005 §4).
const LabelPrefix = "odooboat."

const (
	LabelSchema             = LabelPrefix + "schema"
	LabelScope              = LabelPrefix + "scope"
	LabelWorkspace          = LabelPrefix + "workspace"
	LabelEnvironment        = LabelPrefix + "environment"
	LabelRole               = LabelPrefix + "role"
	LabelOperation          = LabelPrefix + "operation"
	LabelSpec               = LabelPrefix + "spec"
	LabelRequires           = LabelPrefix + "requires"
	LabelPostgresMajor      = LabelPrefix + "data.postgres-major"
	LabelDisplayProject     = LabelPrefix + "display.project"
	LabelDisplayEnvironment = LabelPrefix + "display.environment"
	LabelCreatedBy          = LabelPrefix + "created-by"
)

// Key classes below the reserved prefix (ADR 0005 A2). The core is decoded
// strictly; facts, informational keys, and image metadata follow their own rules.
const (
	FactPrefix          = LabelPrefix + "data."
	DisplayPrefix       = LabelPrefix + "display."
	ImageMetadataPrefix = LabelPrefix + "image."
)

// KnownFeatures lists the critical extensions this binary understands. A
// resource whose odooboat.requires names any other feature is read-only.
var KnownFeatures = []string{}

var ErrInvalidOwnership = errors.New("ownership is invalid")

type ResourceType string

const (
	ResourceContainer ResourceType = "container"
	ResourceVolume    ResourceType = "volume"
	ResourceNetwork   ResourceType = "network"
)

type Scope string

const (
	ScopeEnvironment Scope = "environment"
	ScopePlatform    Scope = "platform"
)

type Role string

const (
	RolePostgres     Role = "postgres"
	RoleOdoo         Role = "odoo"
	RoleJob          Role = "job"
	RolePostgresData Role = "postgres-data"
	RoleOdooData     Role = "odoo-data"
	RoleEnvironment  Role = "environment"
)

var roles = map[ResourceType][]Role{
	ResourceContainer: {RolePostgres, RoleOdoo, RoleJob},
	ResourceVolume:    {RolePostgresData, RoleOdooData},
	ResourceNetwork:   {RoleEnvironment},
}

// Ownership is the decoded form of the ownership labels. It holds only facts
// fixed when the resource is created; it never describes desired state.
type Ownership struct {
	Scope         Scope
	WorkspaceID   string
	EnvironmentID string
	Role          Role
	OperationID   string
	Spec          string
	// Requires lists critical extensions a reader must understand to mutate
	// the resource safely.
	Requires []string
	// PostgresMajor is a known fact; zero means the fact is unknown, never a
	// default (ADR 0005 A2.4).
	PostgresMajor      int
	DisplayProject     string
	DisplayEnvironment string
	CreatedBy          string
	// UnrecognizedFacts holds odooboat.data.* keys this binary does not know.
	// They are reported for diagnostics and never used in decisions.
	UnrecognizedFacts map[string]string
}

type ClaimState string

const (
	// ClaimUnmanaged: no odooboat.* label; the resource is not claimed.
	ClaimUnmanaged ClaimState = "unmanaged"
	// ClaimValid: the labels decode under a supported schema.
	ClaimValid ClaimState = "valid"
	// ClaimNewerSchema: written by a newer Odooboat; read-only.
	ClaimNewerSchema ClaimState = "newer-schema"
	// ClaimMalformed: claimed but unreadable; never mutated.
	ClaimMalformed ClaimState = "malformed"
	// ClaimRequiresUnknown: odooboat.requires names a feature this binary does
	// not know; read-only. The ownership is decoded so status can report it.
	ClaimRequiresUnknown ClaimState = "requires-unknown"
)

// Claim is the result of decoding the labels of an observed resource.
type Claim struct {
	State     ClaimState
	Schema    int
	Ownership Ownership
	Err       error
}

var (
	specRe      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	createdByRe = regexp.MustCompile(`^odooboat/[0-9A-Za-z.+-]+$`)
	positiveRe  = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	featureRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
)

const maxDisplayLength = 128

var knownLabels = []string{
	LabelSchema, LabelScope, LabelWorkspace, LabelEnvironment, LabelRole, LabelOperation,
	LabelSpec, LabelRequires, LabelPostgresMajor, LabelDisplayProject, LabelDisplayEnvironment, LabelCreatedBy,
}

// Labels validates the ownership for a resource type and encodes it. A binary
// only writes features it understands and never writes unrecognized facts.
func (o Ownership) Labels(t ResourceType) (map[string]string, error) {
	if err := o.Validate(t); err != nil {
		return nil, err
	}
	if unknown := unknownFeatures(o.Requires); len(unknown) > 0 {
		return nil, invalid(LabelRequires, "names features unknown to this binary: %s", strings.Join(unknown, ", "))
	}
	if len(o.UnrecognizedFacts) > 0 {
		return nil, invalid(FactPrefix+"*", "unrecognized facts are never written")
	}
	labels := map[string]string{
		LabelSchema:         strconv.Itoa(OwnershipSchema),
		LabelScope:          string(o.Scope),
		LabelWorkspace:      o.WorkspaceID,
		LabelRole:           string(o.Role),
		LabelOperation:      o.OperationID,
		LabelDisplayProject: o.DisplayProject,
		LabelCreatedBy:      o.CreatedBy,
	}
	if o.Scope == ScopeEnvironment {
		labels[LabelEnvironment] = o.EnvironmentID
		labels[LabelDisplayEnvironment] = o.DisplayEnvironment
	}
	if o.Spec != "" {
		labels[LabelSpec] = o.Spec
	}
	if len(o.Requires) > 0 {
		labels[LabelRequires] = strings.Join(o.Requires, ",")
	}
	if o.PostgresMajor != 0 {
		labels[LabelPostgresMajor] = strconv.Itoa(o.PostgresMajor)
	}
	return labels, nil
}

// Validate applies the schema 1 rules for a resource type.
func (o Ownership) Validate(t ResourceType) error {
	vocabulary, ok := roles[t]
	if !ok {
		return invalid("", "unknown resource type %q", t)
	}
	switch o.Scope {
	case ScopeEnvironment:
		if !workspace.ValidUUID(o.EnvironmentID) {
			return invalid(LabelEnvironment, "must be a UUID for environment scope")
		}
		if err := validDisplay(LabelDisplayEnvironment, o.DisplayEnvironment); err != nil {
			return err
		}
	case ScopePlatform:
		if o.EnvironmentID != "" || o.DisplayEnvironment != "" {
			return invalid(LabelEnvironment, "must be absent for platform scope")
		}
	default:
		return invalid(LabelScope, "unknown scope %q", o.Scope)
	}
	if !workspace.ValidUUID(o.WorkspaceID) {
		return invalid(LabelWorkspace, "must be a UUID")
	}
	if !slices.Contains(vocabulary, o.Role) {
		return invalid(LabelRole, "role %q is not valid for a %s", o.Role, t)
	}
	if !workspace.ValidUUID(o.OperationID) {
		return invalid(LabelOperation, "must be a UUID")
	}
	if o.Spec != "" && (t != ResourceContainer || !specRe.MatchString(o.Spec)) {
		return invalid(LabelSpec, "must be sha256:<hex> on a container")
	}
	if err := validFeatures(o.Requires); err != nil {
		return err
	}
	wantsMajor := t == ResourceVolume && o.Role == RolePostgresData
	if wantsMajor != (o.PostgresMajor != 0) || o.PostgresMajor < 0 {
		return invalid(LabelPostgresMajor, "is required on, and only on, a postgres-data volume")
	}
	if err := validDisplay(LabelDisplayProject, o.DisplayProject); err != nil {
		return err
	}
	if !createdByRe.MatchString(o.CreatedBy) {
		return invalid(LabelCreatedBy, "must be odooboat/<version>")
	}
	return nil
}

// DecodeOwnership classifies and strictly decodes the labels of an observed
// resource. Labels outside the odooboat. prefix are ignored, and so is image
// metadata, which a container inherits from its image (ADR 0005 A2): a
// resource carrying only odooboat.image.* labels is unmanaged.
func DecodeOwnership(t ResourceType, labels map[string]string) Claim {
	claimed := false
	for key := range labels {
		if strings.HasPrefix(key, LabelPrefix) && !strings.HasPrefix(key, ImageMetadataPrefix) {
			claimed = true
			break
		}
	}
	if !claimed {
		return Claim{State: ClaimUnmanaged}
	}
	raw, ok := labels[LabelSchema]
	if !ok {
		return malformed(0, invalid(LabelSchema, "is missing"))
	}
	schema, ok := canonicalPositive(raw)
	if !ok {
		return malformed(0, invalid(LabelSchema, "must be a positive integer, got %q", raw))
	}
	if schema > OwnershipSchema {
		return Claim{State: ClaimNewerSchema, Schema: schema}
	}
	o := Ownership{
		Scope:              Scope(labels[LabelScope]),
		WorkspaceID:        labels[LabelWorkspace],
		EnvironmentID:      labels[LabelEnvironment],
		Role:               Role(labels[LabelRole]),
		OperationID:        labels[LabelOperation],
		Spec:               labels[LabelSpec],
		DisplayProject:     labels[LabelDisplayProject],
		DisplayEnvironment: labels[LabelDisplayEnvironment],
		CreatedBy:          labels[LabelCreatedBy],
	}
	for key, value := range labels {
		if !strings.HasPrefix(key, LabelPrefix) || slices.Contains(knownLabels, key) {
			continue
		}
		switch {
		case strings.HasPrefix(key, ImageMetadataPrefix), strings.HasPrefix(key, DisplayPrefix):
			// Inherited image metadata and unknown informational keys are ignored.
		case strings.HasPrefix(key, FactPrefix):
			if o.UnrecognizedFacts == nil {
				o.UnrecognizedFacts = map[string]string{}
			}
			o.UnrecognizedFacts[key] = value
		default:
			return malformed(schema, invalid(key, "is not defined by schema %d", schema))
		}
	}
	if raw, ok := labels[LabelPostgresMajor]; ok {
		if o.PostgresMajor, ok = canonicalPositive(raw); !ok {
			return malformed(schema, invalid(LabelPostgresMajor, "must be a positive integer, got %q", raw))
		}
	}
	if raw, ok := labels[LabelRequires]; ok {
		// An empty list is written by omitting the key, so "" is malformed.
		if raw == "" {
			return malformed(schema, invalid(LabelRequires, "is present but empty"))
		}
		o.Requires = strings.Split(raw, ",")
	}
	// Present but empty values would otherwise decode as absent.
	for _, key := range []string{LabelEnvironment, LabelSpec, LabelDisplayEnvironment} {
		if value, ok := labels[key]; ok && value == "" {
			return malformed(schema, invalid(key, "is present but empty"))
		}
	}
	if err := o.Validate(t); err != nil {
		return malformed(schema, err)
	}
	if unknown := unknownFeatures(o.Requires); len(unknown) > 0 {
		return Claim{State: ClaimRequiresUnknown, Schema: schema, Ownership: o,
			Err: fmt.Errorf("resource requires features unknown to this binary: %s; upgrade odooboat to modify it",
				strings.Join(unknown, ", "))}
	}
	return Claim{State: ClaimValid, Schema: schema, Ownership: o}
}

// CheckImageLabels rejects an image whose labels claim ownership. Images may
// only carry odooboat.image.* metadata; anything else under the reserved
// prefix would be copied onto every container created from the image.
func CheckImageLabels(labels map[string]string) error {
	var offending []string
	for key := range labels {
		if strings.HasPrefix(key, LabelPrefix) && !strings.HasPrefix(key, ImageMetadataPrefix) {
			offending = append(offending, key)
		}
	}
	if len(offending) == 0 {
		return nil
	}
	slices.Sort(offending)
	return invalid("", "image labels must not claim ownership: %s", strings.Join(offending, ", "))
}

func validFeatures(features []string) error {
	for i, feature := range features {
		if !featureRe.MatchString(feature) {
			return invalid(LabelRequires, "feature %q must use lowercase letters, digits, \".\" and \"-\"", feature)
		}
		if slices.Contains(features[:i], feature) {
			return invalid(LabelRequires, "feature %q is listed twice", feature)
		}
	}
	return nil
}

func unknownFeatures(features []string) []string {
	var unknown []string
	for _, feature := range features {
		if !slices.Contains(KnownFeatures, feature) {
			unknown = append(unknown, feature)
		}
	}
	return unknown
}

func canonicalPositive(value string) (int, bool) {
	if !positiveRe.MatchString(value) {
		return 0, false
	}
	n, err := strconv.Atoi(value)
	return n, err == nil
}

func validDisplay(key, value string) error {
	if value == "" || len(value) > maxDisplayLength || !utf8.ValidString(value) {
		return invalid(key, "must be non-empty UTF-8 of at most %d bytes", maxDisplayLength)
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return invalid(key, "must not contain control characters")
	}
	return nil
}

func malformed(schema int, err error) Claim {
	return Claim{State: ClaimMalformed, Schema: schema, Err: err}
}

func invalid(key, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	if key != "" {
		message = key + " " + message
	}
	return fmt.Errorf("%w: %s", ErrInvalidOwnership, message)
}
