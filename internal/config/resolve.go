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

// ResolvePreferences resolves the preferences from the user document alone,
// discovered as Resolve discovers it. It never reads the project, so the CLI
// can choose its presentation before a command resolves anything else.
func ResolvePreferences(opts Options) (ResolvedPreferences, error) {
	workingDir, err := workingDirectory(opts.WorkingDir)
	if err != nil {
		return ResolvedPreferences{}, err
	}
	path, err := discoverUser(workingDir, opts.UserConfigPath)
	if err != nil {
		return ResolvedPreferences{}, err
	}
	var user *loaded[UserDocument]
	if path != "" {
		if user, err = decodeFile[UserDocument](SourceUser, path); err != nil {
			return ResolvedPreferences{}, err
		}
	}
	return resolvePreferences(Provenance{}, user), nil
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
	version := &scalarBuilder[string]{prov: prov, field: FieldOdooVersion}
	version.apply(&project.doc.Project.OdooVersion, projectRef.ref("project.odooVersion"))
	if envOverlay != nil {
		version.apply(envOverlay.OdooVersion, envRef.ref(envPrefix+".odooVersion"))
	}
	if localEnvOverlay != nil {
		version.apply(localEnvOverlay.OdooVersion, localRef.ref(envPrefix+".odooVersion"))
	}
	version.apply(patch.OdooVersion, SourceRef{Kind: SourceInvocation, Field: FieldOdooVersion})
	cfg.OdooVersion = version.value

	matrix, err := EmbeddedMatrix()
	if err != nil {
		return Result{}, err
	}
	entry, seriesKnown := matrix.Lookup(cfg.OdooVersion)
	resolveRuntimeVersions(&cfg, prov, matrix, entry, seriesKnown, runtimeSources{
		project: project.doc.Project, env: envOverlay, localEnv: localEnvOverlay, patch: patch,
		projectRef: projectRef, envRef: envRef, localRef: localRef, envPrefix: envPrefix,
	})

	allowed := &listBuilder{prov: prov, field: FieldAllowUnsupported}
	allowed.apply(&[]string{}, SourceRef{Kind: SourceDefault, Field: FieldAllowUnsupported})
	allowed.apply(project.doc.Project.AllowUnsupported, projectRef.ref("project.allowUnsupported"))
	baseImage := &scalarBuilder[string]{prov: prov, field: FieldBaseImage}
	baseImage.apply(new(string), SourceRef{Kind: SourceDefault, Field: FieldBaseImage})
	bases, err := EmbeddedBases()
	if err != nil {
		return Result{}, err
	}
	if published, ref, ok := bases.Lookup(cfg.OdooVersion, cfg.Python); ok {
		baseImage.apply(&published, ref)
	}
	baseImage.apply(project.doc.Project.BaseImage, projectRef.ref("project.baseImage"))
	if envOverlay != nil {
		allowed.apply(envOverlay.AllowUnsupported, envRef.ref(envPrefix+".allowUnsupported"))
		baseImage.apply(envOverlay.BaseImage, envRef.ref(envPrefix+".baseImage"))
	}
	if local != nil {
		allowed.apply(local.doc.Project.AllowUnsupported, localRef.ref("project.allowUnsupported"))
		baseImage.apply(local.doc.Project.BaseImage, localRef.ref("project.baseImage"))
	}
	if localEnvOverlay != nil {
		allowed.apply(localEnvOverlay.AllowUnsupported, localRef.ref(envPrefix+".allowUnsupported"))
		baseImage.apply(localEnvOverlay.BaseImage, localRef.ref(envPrefix+".baseImage"))
	}
	cfg.AllowUnsupported = toDimensions(allowed.value)
	cfg.BaseImage = baseImage.value

	cfg.Unsupported = []Dimension{}
	if seriesKnown {
		if dims := entry.Unsupported(cfg.Python, cfg.Postgres); dims != nil {
			cfg.Unsupported = dims
		}
	}

	connection := &scalarBuilder[string]{prov: prov, field: FieldRuntimeConnection}
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
	odooSource := &sourceBuilder{prov: prov}
	odooSource.apply(project.doc.Project.Odoo.Source, projectRef, "project.odoo.source")
	if envOverlay != nil && envOverlay.Odoo != nil {
		odooSource.apply(envOverlay.Odoo.Source, envRef, envPrefix+".odoo.source")
	}
	if local != nil && local.doc.Project.Odoo != nil {
		odooSource.apply(local.doc.Project.Odoo.Source, localRef, "project.odoo.source")
	}
	if localEnvOverlay != nil && localEnvOverlay.Odoo != nil {
		odooSource.apply(localEnvOverlay.Odoo.Source, localRef, envPrefix+".odoo.source")
	}
	cfg.Odoo = ResolvedOdoo{MasterPassword: secret.value, Options: options.entries, Source: odooSource.result()}
	prov.markSecret(FieldOdooMasterPassword)

	cfg.Preferences = resolvePreferences(prov, user)

	if err := validateResolved(cfg, prov, user, matrix, seriesKnown); err != nil {
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

// runtimeSources gathers the owners of python and postgres: the same owners as
// odooVersion (ADR 0008 §10).
type runtimeSources struct {
	project                      ProjectDefinition
	env                          *EnvironmentOverlay
	localEnv                     *LocalEnvironmentOverlay
	patch                        InvocationPatch
	projectRef, envRef, localRef docRef
	envPrefix                    string
}

// resolveRuntimeVersions applies the matrix defaults of the resolved series
// first, so authored values override them and provenance names the matrix when
// nothing was authored. An unknown series has no defaults; validation reports
// the series alone rather than a cascade of empty versions.
func resolveRuntimeVersions(cfg *ResolvedConfig, prov Provenance, matrix Matrix, entry SeriesEntry, seriesKnown bool, s runtimeSources) {
	python := &scalarBuilder[string]{prov: prov, field: FieldPython}
	postgres := &scalarBuilder[int]{prov: prov, field: FieldPostgres}
	if seriesKnown {
		python.apply(&entry.DefaultPython, matrix.defaultRef(entry.Series, "python"))
		postgres.apply(&entry.DefaultPostgres, matrix.defaultRef(entry.Series, "postgres"))
	}
	python.apply(s.project.Python, s.projectRef.ref("project.python"))
	postgres.apply(s.project.Postgres, s.projectRef.ref("project.postgres"))
	if s.env != nil {
		python.apply(s.env.Python, s.envRef.ref(s.envPrefix+".python"))
		postgres.apply(s.env.Postgres, s.envRef.ref(s.envPrefix+".postgres"))
	}
	if s.localEnv != nil {
		python.apply(s.localEnv.Python, s.localRef.ref(s.envPrefix+".python"))
		postgres.apply(s.localEnv.Postgres, s.localRef.ref(s.envPrefix+".postgres"))
	}
	python.apply(s.patch.Python, SourceRef{Kind: SourceInvocation, Field: FieldPython})
	postgres.apply(s.patch.Postgres, SourceRef{Kind: SourceInvocation, Field: FieldPostgres})
	cfg.Python = python.value
	cfg.Postgres = postgres.value
}

func toDimensions(values []string) []Dimension {
	out := make([]Dimension, len(values))
	for i, value := range values {
		out[i] = Dimension(value)
	}
	return out
}

// --- Per-field merge helpers

type scalarBuilder[T any] struct {
	prov  Provenance
	field FieldPath
	value T
}

func (b *scalarBuilder[T]) apply(value *T, ref SourceRef) {
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

// sourceBuilder replaces each Odoo source entry as a whole: mixing the path of
// one owner with the commit of another would describe a source nobody wrote.
type sourceBuilder struct {
	prov       Provenance
	community  *ResolvedSourceLocation
	enterprise *ResolvedSourceLocation
}

func (b *sourceBuilder) apply(source *OdooSource, doc docRef, prefix string) {
	if source == nil {
		return
	}
	if source.Community != nil {
		b.community = resolveLocation(doc, *source.Community)
		b.prov.record(FieldOdooCommunity, doc.ref(prefix+".community"))
	}
	if source.Enterprise != nil {
		b.enterprise = resolveLocation(doc, *source.Enterprise)
		b.prov.record(FieldOdooEnterprise, doc.ref(prefix+".enterprise"))
	}
}

func (b *sourceBuilder) result() ResolvedOdooSource {
	out := ResolvedOdooSource{Community: b.community, Enterprise: b.enterprise}
	if b.community != nil {
		out.Mode = b.community.Mode()
	}
	return out
}

func resolveLocation(doc docRef, location SourceLocation) *ResolvedSourceLocation {
	return &ResolvedSourceLocation{
		Path:   resolveRelative(doc.dir, location.Path),
		Git:    location.Git,
		Commit: location.Commit,
	}
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
