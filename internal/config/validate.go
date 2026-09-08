package config

import (
	"fmt"
	"os"
	"regexp"
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
	versionRe    = regexp.MustCompile(`^(saas-)?[0-9]+\.[0-9]+$`)
	envVarRe     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	odooOptionRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
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
		case RuntimeKindDocker, RuntimeKindPodman, RuntimeKindApple:
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
		if *output != OutputYAML && *output != OutputJSON {
			c.addField(ErrAuthored, FieldPreferencesOutput, ref.ref("preferences.output"),
				"unknown output format", *output, "one of yaml, json")
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
	validateAddons(c, ref, "project.addons", doc.doc.Project.Addons, false)

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
		if overlay.RuntimeConnection != nil {
			validateConnectionName(c, ref, prefix+".runtimeConnection", *overlay.RuntimeConnection)
		}
		validateAddons(c, ref, prefix+".addons", overlay.Addons, false)
	}
	return c.err()
}

func validateVersion(c *collector, ref docRef, authored, value string) {
	if !versionRe.MatchString(value) {
		c.addField(ErrAuthored, FieldOdooVersion, ref.ref(authored),
			"odoo version must look like \"18.0\" or \"saas-16.3\"", value,
			"quote the value so YAML does not read it as a number")
	}
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

func validateResolved(cfg ResolvedConfig, prov Provenance, user *loaded[UserDocument]) error {
	c := &collector{}

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

func keySet[V any](m map[string]V) map[string]struct{} {
	set := make(map[string]struct{}, len(m))
	for key := range m {
		set[key] = struct{}{}
	}
	return set
}
