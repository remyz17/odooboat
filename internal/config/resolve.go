package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
)

type Options struct {
	WorkingDir      string
	UserConfigPath  string
	ProjectPath     string
	EnvironmentName string
	Patch           InvocationPatch
}

type Result struct {
	Config     ResolvedConfig
	Provenance Provenance
	Sources    Sources
}

// Resolve runs the full pipeline: discover, decode, validate authored input,
// normalize source-relative values, select the environment, apply field rules,
// apply defaults, then validate the result. It performs no runtime inspection or
// mutation and returns a fresh result per call.
func Resolve(opts Options) (Result, error) {
	sources, err := Discover(DiscoverOptions{
		WorkingDir:      opts.WorkingDir,
		UserConfigPath:  opts.UserConfigPath,
		ProjectPath:     opts.ProjectPath,
		EnvironmentName: opts.EnvironmentName,
		RequireProject:  true,
	})
	if err != nil {
		return Result{}, err
	}
	return resolveSources(sources, opts.Patch)
}

func resolveSources(sources Sources, patch InvocationPatch) (Result, error) {
	user, project, local, err := decodeSources(sources)
	if err != nil {
		return Result{}, err
	}

	authored := &collector{}
	authored.add(validateUserDocument(user))
	authored.add(validateProjectDocument(project))
	authored.add(validateLocalDocument(local))
	if err := authored.err(); err != nil {
		return Result{}, err
	}

	env := sources.EnvironmentName
	envOverlay, localEnvOverlay, err := selectEnvironment(project, local, env)
	if err != nil {
		return Result{}, err
	}

	prov := Provenance{}
	projectRef := project.as(SourceProject)
	envRef := project.as(SourceEnvironment)
	localRef := local.as(SourceLocal)
	envPrefix := "environments." + env

	cfg := ResolvedConfig{
		ProjectName:     project.doc.Project.Name,
		ProjectFile:     project.path,
		EnvironmentName: env,
	}
	prov.record(FieldProjectName, projectRef.ref("project.name"))

	// Scalars: an explicitly present value replaces, an omitted one inherits.
	version := &scalarBuilder{prov: prov, field: FieldOdooVersion}
	version.apply(&project.doc.Project.OdooVersion, projectRef.ref("project.odooVersion"))
	if envOverlay != nil {
		version.apply(envOverlay.OdooVersion, envRef.ref(envPrefix+".odooVersion"))
	}
	if localEnvOverlay != nil {
		version.apply(localEnvOverlay.OdooVersion, localRef.ref(envPrefix+".odooVersion"))
	}
	version.apply(patch.OdooVersion, SourceRef{Kind: SourceInvocation, Field: FieldOdooVersion})
	cfg.OdooVersion = version.value

	connection := &scalarBuilder{prov: prov, field: FieldRuntimeConnection}
	if project.doc.Project.RuntimeConnection != "" {
		connection.apply(&project.doc.Project.RuntimeConnection, projectRef.ref("project.runtimeConnection"))
	}
	if envOverlay != nil {
		connection.apply(envOverlay.RuntimeConnection, envRef.ref(envPrefix+".runtimeConnection"))
	}
	if local != nil {
		connection.apply(local.doc.Project.RuntimeConnection, localRef.ref("project.runtimeConnection"))
	}
	if localEnvOverlay != nil {
		connection.apply(localEnvOverlay.RuntimeConnection, localRef.ref(envPrefix+".runtimeConnection"))
	}
	connection.apply(patch.RuntimeConnection, SourceRef{Kind: SourceInvocation, Field: FieldRuntimeConnection})
	cfg.RuntimeConnection = resolveRuntimeConnection(user, connection.value)

	// Lists replace wholesale; an authored empty list clears the inherited one.
	packages := &listBuilder{prov: prov, field: FieldPythonPackages}
	packages.apply(&[]string{}, SourceRef{Kind: SourceDefault, Field: FieldPythonPackages})
	packages.apply(project.doc.Project.PythonPackages, projectRef.ref("project.pythonPackages"))
	if envOverlay != nil {
		packages.apply(envOverlay.PythonPackages, envRef.ref(envPrefix+".pythonPackages"))
	}
	cfg.PythonPackages = packages.value

	// Keyed collections merge by name: a higher source may add an entry or
	// override individual declared fields of an inherited one.
	addons := newAddonMerger(prov)
	addons.apply(project.doc.Project.Addons, projectRef, "project.addons")
	if envOverlay != nil {
		addons.apply(envOverlay.Addons, envRef, envPrefix+".addons")
	}
	if local != nil {
		addons.apply(local.doc.Project.Addons, localRef, "project.addons")
	}
	if localEnvOverlay != nil {
		addons.apply(localEnvOverlay.Addons, localRef, envPrefix+".addons")
	}
	cfg.Addons = addons.result()

	options := newOptionMerger(prov)
	options.apply(project.doc.Project.Odoo.Options, projectRef, "project.odoo.options")
	secret := &secretBuilder{prov: prov}
	secret.apply(project.doc.Project.Odoo.MasterPassword, projectRef.ref("project.odoo.masterPassword"))
	if envOverlay != nil && envOverlay.Odoo != nil {
		options.apply(envOverlay.Odoo.Options, envRef, envPrefix+".odoo.options")
		secret.apply(envOverlay.Odoo.MasterPassword, envRef.ref(envPrefix+".odoo.masterPassword"))
	}
	cfg.Odoo = ResolvedOdoo{MasterPassword: secret.value, Options: options.entries}
	prov.markSecret(FieldOdooMasterPassword)

	cfg.Preferences = resolvePreferences(prov, user)

	if err := validateResolved(cfg, prov, user); err != nil {
		return Result{}, err
	}
	return Result{Config: cfg, Provenance: prov, Sources: sources}, nil
}

func decodeSources(sources Sources) (*loaded[UserDocument], *loaded[ProjectDocument], *loaded[LocalDocument], error) {
	var user *loaded[UserDocument]
	if sources.UserPath != "" {
		doc, err := decodeFile[UserDocument](SourceUser, sources.UserPath)
		if err != nil {
			return nil, nil, nil, err
		}
		user = doc
	}

	project, err := decodeFile[ProjectDocument](SourceProject, sources.ProjectPath)
	if err != nil {
		return nil, nil, nil, err
	}

	var local *loaded[LocalDocument]
	if sources.LocalPath != "" {
		doc, err := decodeFile[LocalDocument](SourceLocal, sources.LocalPath)
		if err != nil {
			return nil, nil, nil, err
		}
		local = doc
	}
	return user, project, local, nil
}

// selectEnvironment allows a local-only environment; the merged result is held to
// the same validation as a committed one.
func selectEnvironment(project *loaded[ProjectDocument], local *loaded[LocalDocument], env string) (*EnvironmentOverlay, *LocalEnvironmentOverlay, error) {
	var overlay *EnvironmentOverlay
	if value, ok := project.doc.Environments[env]; ok {
		overlay = &value
	}
	var localOverlay *LocalEnvironmentOverlay
	if local != nil {
		if value, ok := local.doc.Environments[env]; ok {
			localOverlay = &value
		}
	}
	if overlay == nil && localOverlay == nil && env != DefaultEnvironment {
		return nil, nil, fieldErr(ErrResolved, FieldPath("environments."+env),
			project.ref("environments"),
			fmt.Sprintf("environment %q is not defined", env),
			"",
			"available environments: "+listEnvironments(project, local))
	}
	return overlay, localOverlay, nil
}

func listEnvironments(project *loaded[ProjectDocument], local *loaded[LocalDocument]) string {
	names := map[string]struct{}{DefaultEnvironment: {}}
	for name := range project.doc.Environments {
		names[name] = struct{}{}
	}
	if local != nil {
		for name := range local.doc.Environments {
			names[name] = struct{}{}
		}
	}
	return joinSorted(names)
}

func resolveRuntimeConnection(user *loaded[UserDocument], name string) ResolvedRuntimeConnection {
	resolved := ResolvedRuntimeConnection{Name: name}
	if user == nil || name == "" {
		return resolved
	}
	entry, ok := user.doc.RuntimeConnections[name]
	if !ok {
		return resolved
	}
	resolved.Kind = entry.Kind
	resolved.Context = entry.Context
	resolved.Socket = normalizeSocket(user.dir, entry.Socket)
	return resolved
}

func normalizeSocket(base, value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme == "unix" && parsed.Host == "" && parsed.Path != "" {
		return "unix://" + filepath.Clean(parsed.Path)
	}
	return resolveRelative(base, value)
}

func resolvePreferences(prov Provenance, user *loaded[UserDocument]) ResolvedPreferences {
	prefs := ResolvedPreferences{Output: OutputYAML, Color: true}
	prov.record(FieldPreferencesOutput, SourceRef{Kind: SourceDefault, Field: FieldPreferencesOutput})
	prov.record(FieldPreferencesColor, SourceRef{Kind: SourceDefault, Field: FieldPreferencesColor})
	if user == nil {
		return prefs
	}
	userRef := user.as(SourceUser)
	if value := user.doc.Preferences.Output; value != nil {
		prefs.Output = *value
		prov.record(FieldPreferencesOutput, userRef.ref("preferences.output"))
	}
	if value := user.doc.Preferences.Color; value != nil {
		prefs.Color = *value
		prov.record(FieldPreferencesColor, userRef.ref("preferences.color"))
	}
	return prefs
}

// --- Per-field merge helpers

type scalarBuilder struct {
	prov  Provenance
	field FieldPath
	value string
}

func (b *scalarBuilder) apply(value *string, ref SourceRef) {
	if value == nil {
		return
	}
	b.value = *value
	b.prov.record(b.field, ref)
}

type listBuilder struct {
	prov     Provenance
	field    FieldPath
	value    []string
	assigned bool
}

func (b *listBuilder) apply(value *[]string, ref SourceRef) {
	if value == nil {
		return
	}
	b.value = append([]string(nil), *value...)
	if b.value == nil {
		b.value = []string{}
	}
	if b.assigned {
		b.prov.markListReplaced(b.field)
	}
	b.assigned = true
	b.prov.record(b.field, ref)
}

type secretBuilder struct {
	prov  Provenance
	value ResolvedSecret
}

func (b *secretBuilder) apply(ref *SecretRef, src SourceRef) {
	if ref == nil {
		return
	}
	if ref.Env != "" {
		b.value = newEnvSecret(ref.Env)
	} else {
		b.value = newLiteralSecret(ref.Literal)
	}
	b.prov.record(FieldOdooMasterPassword, src)
}

type addonState struct {
	path    string
	enabled bool
}

type addonMerger struct {
	prov    Provenance
	entries map[string]*addonState
}

func newAddonMerger(prov Provenance) *addonMerger {
	return &addonMerger{prov: prov, entries: map[string]*addonState{}}
}

func (m *addonMerger) apply(entries map[string]AddonSource, doc docRef, prefix string) {
	for _, name := range sortedKeys(entries) {
		entry := entries[name]
		state, ok := m.entries[name]
		if !ok {
			state = &addonState{enabled: true}
			m.entries[name] = state
			m.prov.record(AddonField(name, "enabled"), SourceRef{Kind: SourceDefault, Field: AddonField(name, "enabled")})
		}
		if entry.Path != nil {
			state.path = resolveRelative(doc.dir, *entry.Path)
			m.prov.record(AddonField(name, "path"), doc.ref(prefix+"."+name+".path"))
		}
		if entry.Enabled != nil {
			state.enabled = *entry.Enabled
			m.prov.record(AddonField(name, "enabled"), doc.ref(prefix+"."+name+".enabled"))
		}
	}
}

func (m *addonMerger) result() []ResolvedAddon {
	out := make([]ResolvedAddon, 0, len(m.entries))
	for _, name := range sortedKeys(m.entries) {
		state := m.entries[name]
		out = append(out, ResolvedAddon{Name: name, Path: state.path, Enabled: state.enabled})
	}
	return out
}

type optionMerger struct {
	prov    Provenance
	entries map[string]string
}

func newOptionMerger(prov Provenance) *optionMerger {
	return &optionMerger{prov: prov, entries: map[string]string{}}
}

func (m *optionMerger) apply(entries map[string]string, doc docRef, prefix string) {
	for _, key := range sortedKeys(entries) {
		m.entries[key] = entries[key]
		m.prov.record(OptionField(key), doc.ref(prefix+"."+key))
	}
}

// resolveRelative interprets an authored path against the directory of the
// document that authored it, never the process working directory.
func resolveRelative(base, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinSorted(set map[string]struct{}) string {
	names := sortedKeys(set)
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}
