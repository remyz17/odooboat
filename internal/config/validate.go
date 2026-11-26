package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ProtectedOdooOptions are owned by the orchestrator: Odooboat generates them for
// the container entrypoint and rejects authored overrides. The set grows with the
// Odoo configuration contract.
var ProtectedOdooOptions = map[string]string{
	"db_host":          "the database host is bound by the environment's runtime",
	"db_port":          "the database port is bound by the environment's runtime",
	"db_user":          "database credentials are managed by odooboat",
	"db_password":      "database credentials are managed by odooboat",
	"db_name":          "the database is selected by the active dataset",
	"data_dir":         "the data directory is fixed by the image contract",
	"addons_path":      "use the addons field instead",
	"admin_passwd":     "use odoo.masterPassword instead",
	"http_interface":   "the listening address is owned by the runtime",
	"http_port":        "the listening port is owned by the runtime",
	"longpolling_port": "the listening port is owned by the runtime",
	"gevent_port":      "the listening port is owned by the runtime",
}

var (
	nameRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	versionRe    = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	envVarRe     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	odooOptionRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	commitRe     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// A base image is pinned by digest: a tag is never an identity (ADR 0008
	// §13). A bare image ID designates a local image, for development before
	// publication and for custom bases built locally (ADR 0008 A3).
	pinnedImageRe = regexp.MustCompile(`^([a-z0-9][a-z0-9._/:-]*@)?sha256:[0-9a-f]{64}$`)
)

const nameRule = `must contain only lowercase letters, digits, ".", "-" and "_"`

// --- Authored validation

func validateUserDocument(doc *loaded[UserDocument]) error {
	if doc == nil {
		return nil
	}
	c := &collector{}
	ref := doc.as(SourceUser)

	for _, name := range sortedKeys(doc.doc.RuntimeConnections) {
		entry := doc.doc.RuntimeConnections[name]
		authored := "runtimeConnections." + name
		if !nameRe.MatchString(name) {
			c.addField(ErrAuthored, FieldPath(authored), ref.ref(authored),
				"runtime connection name "+nameRule, name, "")
		}
		switch entry.Kind {
		case RuntimeKindDocker, RuntimeKindPodman:
			if (entry.Context == "") == (entry.Socket == "") {
				c.addField(ErrAuthored, FieldPath(authored), ref.ref(authored),
					"docker and podman connections require exactly one of context or socket", "", "")
			}
		case RuntimeKindApple:
			if entry.Context != "" || entry.Socket != "" {
				c.addField(ErrAuthored, FieldPath(authored), ref.ref(authored),
					"apple connections represent the local runtime and reject context and socket", "", "")
			}
		case "":
			c.addField(ErrAuthored, FieldPath(authored+".kind"), ref.ref(authored),
				"runtime connection kind is required", "",
				"one of docker, podman, apple")
		default:
			c.addField(ErrAuthored, FieldPath(authored+".kind"), ref.ref(authored+".kind"),
				"unknown runtime connection kind", entry.Kind,
				"one of docker, podman, apple")
		}
	}

	if output := doc.doc.Preferences.Output; output != nil {
		if *output != OutputText && *output != OutputYAML && *output != OutputJSON {
			c.addField(ErrAuthored, FieldPreferencesOutput, ref.ref("preferences.output"),
				"unknown output format", *output, "one of text, yaml, json")
		}
	}
	return c.err()
}

func validateProjectDocument(doc *loaded[ProjectDocument]) error {
	c := &collector{}
	ref := doc.as(SourceProject)
	definition := doc.doc.Project

	if definition.Name == "" {
		c.addField(ErrAuthored, FieldProjectName, ref.ref("project"),
			"project name is required", "", "add \"name: my-project\" under \"project\"")
	} else if !nameRe.MatchString(definition.Name) {
		c.addField(ErrAuthored, FieldProjectName, ref.ref("project.name"),
			"project name "+nameRule, definition.Name, "")
	}

	if definition.OdooVersion == "" {
		c.addField(ErrAuthored, FieldOdooVersion, ref.ref("project"),
			"odoo version is required", "", "add \"odooVersion: \\\"18.0\\\"\" under \"project\"")
	} else {
		validateVersion(c, ref, "project.odooVersion", definition.OdooVersion)
	}
	validateRuntimeVersions(c, ref, "project", definition.Python, definition.Postgres)
	validateImageChoices(c, ref, "project", definition.AllowUnsupported, definition.BaseImage)
	validateConnectionName(c, ref, "project.runtimeConnection", definition.RuntimeConnection)
	validateAddons(c, ref, "project.addons", definition.Addons, true)
	validateOdoo(c, ref, "project.odoo", &definition.Odoo)

	for _, env := range sortedKeys(doc.doc.Environments) {
		overlay := doc.doc.Environments[env]
		prefix := "environments." + env
		if !nameRe.MatchString(env) {
			c.addField(ErrAuthored, FieldPath(prefix), ref.ref(prefix),
				"environment name "+nameRule, env, "")
		}
		if overlay.OdooVersion != nil {
			validateVersion(c, ref, prefix+".odooVersion", *overlay.OdooVersion)
		}
		validateRuntimeVersions(c, ref, prefix, overlay.Python, overlay.Postgres)
		validateImageChoices(c, ref, prefix, overlay.AllowUnsupported, overlay.BaseImage)
		if overlay.RuntimeConnection != nil {
			validateConnectionName(c, ref, prefix+".runtimeConnection", *overlay.RuntimeConnection)
		}
		validateAddons(c, ref, prefix+".addons", overlay.Addons, false)
		validateOdoo(c, ref, prefix+".odoo", overlay.Odoo)
	}
	return c.err()
}

func validateLocalDocument(doc *loaded[LocalDocument]) error {
	if doc == nil {
		return nil
	}
	c := &collector{}
	ref := doc.as(SourceLocal)

	if doc.doc.Project.RuntimeConnection != nil {
		validateConnectionName(c, ref, "project.runtimeConnection", *doc.doc.Project.RuntimeConnection)
	}
	validateImageChoices(c, ref, "project", doc.doc.Project.AllowUnsupported, doc.doc.Project.BaseImage)
	validateAddons(c, ref, "project.addons", doc.doc.Project.Addons, false)
	if odoo := doc.doc.Project.Odoo; odoo != nil {
		validateSource(c, ref, "project.odoo.source", odoo.Source)
	}

	for _, env := range sortedKeys(doc.doc.Environments) {
		overlay := doc.doc.Environments[env]
		prefix := "environments." + env
		if !nameRe.MatchString(env) {
			c.addField(ErrAuthored, FieldPath(prefix), ref.ref(prefix),
				"environment name "+nameRule, env, "")
		}
		if overlay.OdooVersion != nil {
			validateVersion(c, ref, prefix+".odooVersion", *overlay.OdooVersion)
		}
		validateRuntimeVersions(c, ref, prefix, overlay.Python, overlay.Postgres)
		validateImageChoices(c, ref, prefix, overlay.AllowUnsupported, overlay.BaseImage)
		if overlay.RuntimeConnection != nil {
			validateConnectionName(c, ref, prefix+".runtimeConnection", *overlay.RuntimeConnection)
		}
		validateAddons(c, ref, prefix+".addons", overlay.Addons, false)
		if overlay.Odoo != nil {
			validateSource(c, ref, prefix+".odoo.source", overlay.Odoo.Source)
		}
	}
	return c.err()
}

func validateVersion(c *collector, ref docRef, authored, value string) {
	if !versionRe.MatchString(value) {
		c.addField(ErrAuthored, FieldOdooVersion, ref.ref(authored),
			"odoo version must look like \"18.0\"", value,
			"quote the value so YAML does not read it as a number")
	}
}

// validateRuntimeVersions checks the format only; whether a version is
// supported depends on the resolved series and is decided by the matrix.
func validateRuntimeVersions(c *collector, ref docRef, prefix string, python *string, postgres *int) {
	if python != nil && !pythonRe.MatchString(*python) {
		c.addField(ErrAuthored, FieldPython, ref.ref(prefix+".python"),
			"python version must look like \"3.12\"", *python,
			"quote the value so YAML does not read 3.10 as 3.1")
	}
	if postgres != nil && *postgres <= 0 {
		c.addField(ErrAuthored, FieldPostgres, ref.ref(prefix+".postgres"),
			"postgres must be a positive major version", strconv.Itoa(*postgres), "for example \"postgres: 16\"")
	}
}

func validateImageChoices(c *collector, ref docRef, prefix string, allowed *[]string, baseImage *string) {
	if allowed != nil {
		for i, value := range *allowed {
			authored := fmt.Sprintf("%s.allowUnsupported[%d]", prefix, i)
			switch {
			case !slices.Contains(dimensions, Dimension(value)):
				c.addField(ErrAuthored, FieldAllowUnsupported, ref.ref(authored),
					"unknown dimension", value,
					"one of python, postgres; the Odoo series itself cannot be relaxed")
			case slices.Contains((*allowed)[:i], value):
				c.addField(ErrAuthored, FieldAllowUnsupported, ref.ref(authored),
					"dimension is listed twice", value, "")
			}
		}
	}
	if baseImage != nil && !pinnedImageRe.MatchString(*baseImage) {
		c.addField(ErrAuthored, FieldBaseImage, ref.ref(prefix+".baseImage"),
			"base image must be pinned by digest", *baseImage,
			"use registry/name@sha256:<digest>, or sha256:<image id> for a local image; a tag is never an identity")
	}
}

func validateSource(c *collector, ref docRef, prefix string, source *OdooSource) {
	if source == nil {
		return
	}
	validateLocation(c, ref, prefix+".community", FieldOdooCommunity, source.Community)
	validateLocation(c, ref, prefix+".enterprise", FieldOdooEnterprise, source.Enterprise)
}

func validateLocation(c *collector, ref docRef, authored string, field FieldPath, location *SourceLocation) {
	if location == nil {
		return
	}
	mount, git := location.Path != "", location.Git != "" || location.Commit != ""
	switch {
	case mount == git:
		c.addField(ErrAuthored, field, ref.ref(authored),
			"a source sets exactly one of \"path\" (mount) or \"git\" with \"commit\" (embedded)", "", "")
	case git && location.Git == "":
		c.addField(ErrAuthored, field, ref.ref(authored+".git"), "git repository URL is required", "", "")
	case git && !commitRe.MatchString(location.Commit):
		c.addField(ErrAuthored, field, ref.ref(authored+".commit"),
			"commit must be a full 40-character lowercase SHA", location.Commit,
			"embedded sources are pinned to an exact revision")
	case git && gitURLHasCredentials(location.Git):
		// The value is not echoed: it contains the credential.
		c.addField(ErrAuthored, field, ref.ref(authored+".git"),
			"git URL must not contain credentials", "",
			"remove them; odooboat fetches with your own git credential helper or SSH agent")
	}
}

// gitURLHasCredentials reports user information in an HTTP(S) URL, where it is
// a password or token. SSH user names such as git@host carry no secret.
func gitURLHasCredentials(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func validateConnectionName(c *collector, ref docRef, authored, value string) {
	if value == "" || nameRe.MatchString(value) {
		return
	}
	c.addField(ErrAuthored, FieldRuntimeConnection, ref.ref(authored),
		"runtime connection name "+nameRule, value, "")
}

// requirePath is true only for the document that declares an addon set; overlays
// may author a subset of the fields.
func validateAddons(c *collector, ref docRef, prefix string, entries map[string]AddonSource, requirePath bool) {
	for _, name := range sortedKeys(entries) {
		entry := entries[name]
		authored := prefix + "." + name
		if !nameRe.MatchString(name) {
			c.addField(ErrAuthored, AddonField(name, "name"), ref.ref(authored),
				"addon set name "+nameRule, name, "")
		}
		switch {
		case entry.Path == nil && requirePath:
			c.addField(ErrAuthored, AddonField(name, "path"), ref.ref(authored),
				"addon path is required", "", "add \"path: ./addons\"")
		case entry.Path != nil && *entry.Path == "":
			c.addField(ErrAuthored, AddonField(name, "path"), ref.ref(authored+".path"),
				"addon path must not be empty", "", "")
		}
	}
}

func validateOdoo(c *collector, ref docRef, prefix string, settings *OdooSettings) {
	if settings == nil {
		return
	}
	validateSource(c, ref, prefix+".source", settings.Source)
	if secret := settings.MasterPassword; secret != nil {
		authored := prefix + ".masterPassword"
		switch {
		case secret.Env == "" && secret.Literal == "":
			c.addField(ErrAuthored, FieldOdooMasterPassword, ref.ref(authored),
				"master password must set exactly one of \"env\" or \"literal\"", "", "")
		case secret.Env != "" && secret.Literal != "":
			c.addField(ErrAuthored, FieldOdooMasterPassword, ref.ref(authored),
				"master password must set exactly one of \"env\" or \"literal\"", "",
				"prefer \"env\" so the secret stays out of source control")
		case secret.Env != "" && !envVarRe.MatchString(secret.Env):
			c.addField(ErrAuthored, FieldOdooMasterPassword, ref.ref(authored+".env"),
				"environment variable names must be uppercase", secret.Env, "")
		}
	}

	for _, key := range sortedKeys(settings.Options) {
		authored := prefix + ".options." + key
		if reason, protected := ProtectedOdooOptions[key]; protected {
			c.addField(ErrAuthored, OptionField(key), ref.ref(authored),
				"this Odoo option is managed by odooboat", key, reason)
			continue
		}
		if !odooOptionRe.MatchString(key) {
			c.addField(ErrAuthored, OptionField(key), ref.ref(authored),
				"odoo option names must be lowercase with underscores", key, "")
		}
	}
}

// --- Resolved validation

func validateResolved(cfg ResolvedConfig, prov Provenance, user *loaded[UserDocument], matrix Matrix, seriesKnown bool) error {
	c := &collector{}
	validateCombination(c, cfg, prov, matrix, seriesKnown)
	validateResolvedSource(c, cfg.Odoo.Source, prov)

	if cfg.RuntimeConnection.Name == "" {
		c.addField(ErrResolved, FieldRuntimeConnection, prov[FieldProjectName].Winner,
			"no runtime connection selected", "",
			"set \"runtimeConnection\" under \"project\" or pass --runtime-connection")
	} else if cfg.RuntimeConnection.Kind == "" {
		hint := "define it in your user configuration"
		if user != nil {
			hint = "available connections: " + joinSorted(keySet(user.doc.RuntimeConnections))
		}
		c.addField(ErrResolved, FieldRuntimeConnection, prov[FieldRuntimeConnection].Winner,
			"unknown runtime connection", cfg.RuntimeConnection.Name, hint)
	}

	seen := map[string]string{}
	for _, addon := range cfg.EnabledAddons() {
		field := AddonField(addon.Name, "path")
		ref := prov[field].Winner
		if other, dup := seen[addon.Path]; dup {
			c.addField(ErrResolved, field, ref,
				fmt.Sprintf("addon path is already used by %q", other), addon.Path, "")
			continue
		}
		seen[addon.Path] = addon.Name

		info, err := os.Stat(addon.Path)
		switch {
		case err != nil:
			c.addField(ErrResolved, field, ref, "addon path does not exist", addon.Path,
				"paths are resolved relative to the file that authored them")
		case !info.IsDir():
			c.addField(ErrResolved, field, ref, "addon path is not a directory", addon.Path, "")
		}
	}
	return c.err()
}

// validateCombination rejects a combination absent from the matrix unless each
// unsupported dimension is explicitly accepted (ADR 0008 §10). The series is
// never relaxable: without a matrix entry there is no base image to build on.
func validateCombination(c *collector, cfg ResolvedConfig, prov Provenance, matrix Matrix, seriesKnown bool) {
	if !seriesKnown {
		c.addField(ErrResolved, FieldOdooVersion, prov[FieldOdooVersion].Winner,
			"odoo series is not supported", cfg.OdooVersion,
			"supported series: "+strings.Join(matrix.Series(), ", "))
		return
	}
	entry, _ := matrix.Lookup(cfg.OdooVersion)
	for _, dim := range cfg.Unsupported {
		if slices.Contains(cfg.AllowUnsupported, dim) {
			continue
		}
		field, value, supported := FieldPython, cfg.Python, strings.Join(entry.Python, ", ")
		if dim == DimensionPostgres {
			field, value = FieldPostgres, strconv.Itoa(cfg.Postgres)
			supported = joinInts(entry.Postgres)
		}
		c.addField(ErrResolved, field, prov[field].Winner,
			fmt.Sprintf("%s %s is not supported for Odoo %s", dim, value, cfg.OdooVersion), "",
			fmt.Sprintf("supported: %s; to accept it deliberately, list %q under allowUnsupported", supported, dim))
	}
}

func validateResolvedSource(c *collector, source ResolvedOdooSource, prov Provenance) {
	if source.Enterprise == nil {
		if source.Community != nil {
			checkMountPath(c, FieldOdooCommunity, prov, source.Community)
		}
		return
	}
	if source.Community == nil {
		c.addField(ErrResolved, FieldOdooEnterprise, prov[FieldOdooEnterprise].Winner,
			"an enterprise source requires a community source", "",
			"community and enterprise form one coherent pair")
		return
	}
	if source.Community.Mode() != source.Enterprise.Mode() {
		c.addField(ErrResolved, FieldOdooEnterprise, prov[FieldOdooEnterprise].Winner,
			fmt.Sprintf("enterprise is %s but community is %s", source.Enterprise.Mode(), source.Community.Mode()), "",
			"community and enterprise must use the same mode: both paths, or both git commits")
		return
	}
	checkMountPath(c, FieldOdooCommunity, prov, source.Community)
	checkMountPath(c, FieldOdooEnterprise, prov, source.Enterprise)
}

func checkMountPath(c *collector, field FieldPath, prov Provenance, location *ResolvedSourceLocation) {
	if location.Path == "" {
		return
	}
	info, err := os.Stat(location.Path)
	switch {
	case err != nil:
		c.addField(ErrResolved, field, prov[field].Winner, "source path does not exist", location.Path,
			"paths are resolved relative to the file that authored them")
	case !info.IsDir():
		c.addField(ErrResolved, field, prov[field].Winner, "source path is not a directory", location.Path, "")
	}
}

func joinInts(values []int) string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strconv.Itoa(value)
	}
	return strings.Join(out, ", ")
}

func keySet[V any](m map[string]V) map[string]struct{} {
	set := make(map[string]struct{}, len(m))
	for key := range m {
		set[key] = struct{}{}
	}
	return set
}
