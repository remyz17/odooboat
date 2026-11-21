// Package envspec derives the resources of a running environment from its
// resolved configuration (ADR 0009): the network, the volumes, the PostgreSQL
// and Odoo containers, and the generated odoo.conf. It is pure: it never talks
// to a runtime, and the application decides what to do with the result.
package envspec

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/runtime"
)

// Fixed locations of the image contract (ADR 0008 A2) and of ADR 0009.
const (
	CommunityDir  = "/opt/odoo/community"
	EnterpriseDir = "/opt/odoo/enterprise"
	AddonsDir     = "/opt/odooboat/addons"
	DataDir       = "/var/lib/odoo"
	RunDir        = "/opt/odooboat/run"
	ConfFile      = "odoo.conf"
	HelperPath    = "/opt/odooboat/helper/odooboat_helper.py"
	// Entrypoint resolves the odoo command and prepares the process
	// environment; commands executed in a running container go through it.
	Entrypoint   = "/opt/odooboat/bin/entrypoint"
	PostgresData = "/var/lib/postgresql/data"

	// HTTPPort is Odoo's port inside the container; the host port is chosen
	// by the engine (ADR 0009 §6).
	HTTPPort = 8069
	// HostAddress is where the Odoo port is published.
	HostAddress  = "127.0.0.1"
	PostgresPort = 5432
	// PostgresUser and PostgresPassword are fixed: the server port is not
	// published and the network is private (ADR 0009 §2).
	PostgresUser     = "odoo"
	PostgresPassword = "odoo"
)

// Identity names the environment and the operation creating resources.
type Identity struct {
	WorkspaceID   string
	EnvironmentID string
	Project       string
	Environment   string
	OperationID   string
	// Version is the odooboat version, recorded in odooboat.created-by.
	Version string
}

// Images are the image IDs the containers run. IDs, not references, so the
// spec hash changes exactly when the content does.
type Images struct {
	Odoo     string
	Postgres string
}

// Inputs are the values resolved by the application before planning.
type Inputs struct {
	Config   config.ResolvedConfig
	Identity Identity
	Images   Images
	// MasterPassword is the resolved masterPassword; Configured is false when
	// none is configured, and admin_passwd is then left to Odoo's default.
	MasterPassword string
	Configured     bool
}

type Network struct {
	Name   string
	Labels map[string]string
}

type Volume struct {
	Name   string
	Role   runtime.Role
	Labels map[string]string
	// PostgresMajor is the fact recorded on postgres-data, zero otherwise.
	PostgresMajor int
}

// File is copied into a created container before it starts.
type File struct {
	Dir     string
	Name    string
	Mode    int64
	Content []byte
}

type Container struct {
	Role runtime.Role
	Spec runtime.ContainerSpec
	// Hash is odooboat.spec: what the container runs, excluding labels that
	// change without changing it (ADR 0009 §4).
	Hash  string
	Files []File
}

// Desired is the complete set of resources of a running environment, in
// creation order.
type Desired struct {
	Network  Network
	Volumes  []Volume
	Postgres Container
	Odoo     Container
	// Warnings are reported by up and status.
	Warnings []string
}

// Containers returns the containers in start order.
func (d Desired) Containers() []Container { return []Container{d.Postgres, d.Odoo} }

// New derives the desired resources.
func New(in Inputs) (Desired, error) {
	cfg, id := in.Config, in.Identity
	if cfg.Odoo.Source.Community == nil {
		return Desired{}, errors.New("envspec: a community source is required")
	}
	if in.Images.Odoo == "" || in.Images.Postgres == "" {
		return Desired{}, errors.New("envspec: both image IDs are required")
	}
	name := func(role runtime.Role) (string, error) {
		return runtime.ResourceName(id.Project, id.Environment, role, id.EnvironmentID)
	}
	labels := func(kind runtime.ResourceType, role runtime.Role, spec string, postgresMajor int) (map[string]string, error) {
		return runtime.Ownership{
			Scope: runtime.ScopeEnvironment, WorkspaceID: id.WorkspaceID, EnvironmentID: id.EnvironmentID,
			Role: role, OperationID: id.OperationID, Spec: spec, PostgresMajor: postgresMajor,
			DisplayProject: id.Project, DisplayEnvironment: id.Environment, CreatedBy: "odooboat/" + id.Version,
		}.Labels(kind)
	}

	var d Desired
	var err error
	if d.Network.Name, err = name(runtime.RoleEnvironment); err != nil {
		return Desired{}, err
	}
	if d.Network.Labels, err = labels(runtime.ResourceNetwork, runtime.RoleEnvironment, "", 0); err != nil {
		return Desired{}, err
	}
	for _, v := range []Volume{{Role: runtime.RolePostgresData, PostgresMajor: cfg.Postgres}, {Role: runtime.RoleOdooData}} {
		if v.Name, err = name(v.Role); err != nil {
			return Desired{}, err
		}
		if v.Labels, err = labels(runtime.ResourceVolume, v.Role, "", v.PostgresMajor); err != nil {
			return Desired{}, err
		}
		d.Volumes = append(d.Volumes, v)
	}
	postgresData, odooData := d.Volumes[0].Name, d.Volumes[1].Name

	postgresName, err := name(runtime.RolePostgres)
	if err != nil {
		return Desired{}, err
	}
	d.Postgres = Container{Role: runtime.RolePostgres, Spec: runtime.ContainerSpec{
		Name: postgresName, Image: in.Images.Postgres, Network: d.Network.Name,
		Env: []string{
			"POSTGRES_USER=" + PostgresUser, "POSTGRES_PASSWORD=" + PostgresPassword, "POSTGRES_DB=postgres",
		},
		Mounts: []runtime.Mount{{Kind: runtime.MountVolume, Source: postgresData, Target: PostgresData}},
	}}

	conf, warnings := OdooConf(cfg, postgresName, in.MasterPassword, in.Configured)
	d.Warnings = warnings
	odooName, err := name(runtime.RoleOdoo)
	if err != nil {
		return Desired{}, err
	}
	odoo := runtime.ContainerSpec{
		Name: odooName, Image: in.Images.Odoo, Network: d.Network.Name,
		Cmd:    []string{"odoo", "--config", path.Join(RunDir, ConfFile)},
		Mounts: []runtime.Mount{{Kind: runtime.MountVolume, Source: odooData, Target: DataDir}},
		Ports:  []runtime.PortBinding{{ContainerPort: HTTPPort, HostIP: HostAddress}},
	}
	src := cfg.Odoo.Source
	if src.Mode == config.SourceModeMount {
		odoo.Mounts = append(odoo.Mounts, runtime.Mount{Kind: runtime.MountBind, Source: src.Community.Path, Target: CommunityDir, ReadOnly: true})
		if src.Enterprise != nil {
			odoo.Mounts = append(odoo.Mounts, runtime.Mount{Kind: runtime.MountBind, Source: src.Enterprise.Path, Target: EnterpriseDir, ReadOnly: true})
		}
	}
	for _, addon := range cfg.Addons {
		if addon.Enabled {
			odoo.Mounts = append(odoo.Mounts, runtime.Mount{Kind: runtime.MountBind, Source: addon.Path, Target: path.Join(AddonsDir, addon.Name), ReadOnly: true})
		}
	}
	d.Odoo = Container{Role: runtime.RoleOdoo, Spec: odoo,
		Files: []File{{Dir: RunDir, Name: ConfFile, Mode: 0o640, Content: conf}}}

	for _, c := range []*Container{&d.Postgres, &d.Odoo} {
		c.Hash = specHash(c.Spec, c.Files)
		if c.Spec.Labels, err = labels(runtime.ResourceContainer, c.Role, c.Hash, 0); err != nil {
			return Desired{}, err
		}
	}
	return d, nil
}

// NeedsBindMounts reports whether the Odoo container mounts host paths, which
// requires the mount.bind capability.
func (d Desired) NeedsBindMounts() bool {
	return slices.ContainsFunc(d.Odoo.Spec.Mounts, func(m runtime.Mount) bool { return m.Kind == runtime.MountBind })
}

// specHash is the SHA-256 of the canonical JSON of what a container runs.
// Labels are excluded: the operation UUID and informational keys change
// without changing the container.
func specHash(spec runtime.ContainerSpec, files []File) string {
	type file struct {
		Dir, Name string
		Mode      int64
		Digest    string
	}
	canonical := struct {
		Image   string                `json:"image"`
		User    string                `json:"user"`
		Env     []string              `json:"env"`
		Cmd     []string              `json:"cmd"`
		Network string                `json:"network"`
		Mounts  []runtime.Mount       `json:"mounts"`
		Ports   []runtime.PortBinding `json:"ports"`
		Files   []file                `json:"files"`
	}{Image: spec.Image, User: spec.User, Env: spec.Env, Cmd: spec.Cmd, Network: spec.Network,
		Mounts: spec.Mounts, Ports: spec.Ports, Files: []file{}}
	for _, f := range files {
		sum := sha256.Sum256(f.Content)
		canonical.Files = append(canonical.Files, file{f.Dir, f.Name, f.Mode, hex.EncodeToString(sum[:])})
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		panic(err) // only plain values: cannot fail
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// OdooConf generates odoo.conf (ADR 0009 §3): the protected keys first, then
// the user's options sorted by key. It returns warnings for up and status.
func OdooConf(cfg config.ResolvedConfig, postgresHost, masterPassword string, configured bool) ([]byte, []string) {
	var warnings []string
	addons := []string{path.Join(CommunityDir, "addons"), path.Join(CommunityDir, "odoo", "addons")}
	if cfg.Odoo.Source.Enterprise != nil {
		addons = append(addons, EnterpriseDir)
	}
	for _, addon := range cfg.Addons {
		if addon.Enabled {
			addons = append(addons, path.Join(AddonsDir, addon.Name))
		}
	}
	lines := [][2]string{
		{"db_host", postgresHost},
		{"db_port", strconv.Itoa(PostgresPort)},
		{"db_user", PostgresUser},
		{"db_password", PostgresPassword},
		{"data_dir", DataDir},
		{"addons_path", strings.Join(addons, ",")},
		{"http_interface", "0.0.0.0"},
		{"http_port", strconv.Itoa(HTTPPort)},
	}
	if configured {
		lines = append(lines, [2]string{"admin_passwd", masterPassword})
	} else {
		warnings = append(warnings, "no masterPassword is configured: Odoo's database manager uses its default master password")
	}
	keys := make([]string, 0, len(cfg.Odoo.Options))
	for key := range cfg.Odoo.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, [2]string{key, cfg.Odoo.Options[key]})
	}
	if workers, err := strconv.Atoi(cfg.Odoo.Options["workers"]); err == nil && workers > 0 {
		warnings = append(warnings, "workers > 0: Odoo serves websockets on its gevent port, which is not published yet")
	}

	var out bytes.Buffer
	out.WriteString("; Generated by odooboat (ADR 0009 §3). Changes are overwritten.\n[options]\n")
	for _, line := range lines {
		fmt.Fprintf(&out, "%s = %s\n", line[0], line[1])
	}
	return out.Bytes(), warnings
}

// Archive packs files for CopyToContainer. The adapter gives them to the
// container's user, so the mode can stay 0640 although odoo.conf holds
// passwords (ADR 0009 §3).
func Archive(files []File) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.Name, Mode: f.Mode, Size: int64(len(f.Content)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
