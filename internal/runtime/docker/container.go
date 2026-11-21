package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/remyz17/odooboat/internal/runtime"
)

var (
	_ runtime.ContainerStore = (*Engine)(nil)
	_ runtime.NetworkStore   = (*Engine)(nil)
	_ runtime.VolumeStore    = (*Engine)(nil)
)

// classify maps engine errors onto runtime classes from their structured
// type; the message is kept for the user, never parsed.
func classify(err error, format string, args ...any) error {
	what := fmt.Sprintf(format, args...)
	switch {
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %s: %v", runtime.ErrNotFound, what, err)
	case cerrdefs.IsConflict(err), cerrdefs.IsAlreadyExists(err):
		return fmt.Errorf("%w: %s: %v", runtime.ErrConflict, what, err)
	default:
		return fmt.Errorf("%s: %w", what, err)
	}
}

func labelFilters(filter runtime.LabelFilter) client.Filters {
	filters := client.Filters{}
	for key, value := range filter {
		filters.Add("label", key+"="+value)
	}
	return filters
}

func (e *Engine) ListContainers(ctx context.Context, filter runtime.LabelFilter) ([]runtime.ContainerInfo, error) {
	result, err := e.client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: labelFilters(filter)})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	containers := make([]runtime.ContainerInfo, 0, len(result.Items))
	for _, item := range result.Items {
		info, err := e.InspectContainer(ctx, item.ID)
		if errors.Is(err, runtime.ErrNotFound) {
			continue // removed between listing and inspection
		}
		if err != nil {
			return nil, err
		}
		containers = append(containers, info)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers, nil
}

func (e *Engine) InspectContainer(ctx context.Context, nameOrID string) (runtime.ContainerInfo, error) {
	result, err := e.client.ContainerInspect(ctx, nameOrID, client.ContainerInspectOptions{})
	if err != nil {
		return runtime.ContainerInfo{}, classify(err, "inspect container %s", nameOrID)
	}
	c := result.Container
	info := runtime.ContainerInfo{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), ImageID: c.Image, State: runtime.ContainerOther}
	if c.Config != nil {
		info.Labels = c.Config.Labels
	}
	if c.State != nil {
		switch c.State.Status {
		case container.StateCreated:
			info.State = runtime.ContainerCreated
		case container.StateRunning:
			info.State = runtime.ContainerRunning
		case container.StateExited:
			info.State, info.ExitCode = runtime.ContainerExited, c.State.ExitCode
		}
	}
	if c.NetworkSettings != nil {
		for port, bindings := range c.NetworkSettings.Ports {
			if port.Proto() != network.TCP {
				continue
			}
			for _, binding := range bindings {
				hostPort, err := strconv.Atoi(binding.HostPort)
				if err != nil {
					continue
				}
				hostIP := ""
				if binding.HostIP.IsValid() {
					hostIP = binding.HostIP.String()
				}
				info.Ports = append(info.Ports, runtime.PortBinding{ContainerPort: int(port.Num()), HostIP: hostIP, HostPort: hostPort})
			}
		}
		sort.Slice(info.Ports, func(i, j int) bool {
			a, b := info.Ports[i], info.Ports[j]
			if a.ContainerPort != b.ContainerPort {
				return a.ContainerPort < b.ContainerPort
			}
			return a.HostIP < b.HostIP
		})
	}
	return info, nil
}

func (e *Engine) CreateContainer(ctx context.Context, spec runtime.ContainerSpec) (string, error) {
	// Creation never pulls (image.no-implicit-pull): an absent image is
	// reported before the engine could try.
	if _, err := e.InspectImage(ctx, spec.Image); err != nil {
		return "", err
	}
	config := &container.Config{Image: spec.Image, User: spec.User, Env: spec.Env, Cmd: spec.Cmd, Labels: spec.Labels}
	host := &container.HostConfig{}
	if spec.Network != "" {
		host.NetworkMode = container.NetworkMode(spec.Network)
	}
	for _, m := range spec.Mounts {
		kind := mount.TypeVolume
		if m.Kind == runtime.MountBind {
			kind = mount.TypeBind
		}
		host.Mounts = append(host.Mounts, mount.Mount{Type: kind, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	if len(spec.Ports) > 0 {
		config.ExposedPorts = network.PortSet{}
		host.PortBindings = network.PortMap{}
		for _, p := range spec.Ports {
			port, err := network.ParsePort(strconv.Itoa(p.ContainerPort) + "/tcp")
			if err != nil {
				return "", err
			}
			binding := network.PortBinding{HostPort: ""}
			if p.HostPort != 0 {
				binding.HostPort = strconv.Itoa(p.HostPort)
			}
			if p.HostIP != "" {
				addr, err := netip.ParseAddr(p.HostIP)
				if err != nil {
					return "", fmt.Errorf("port %d: host address %q: %w", p.ContainerPort, p.HostIP, err)
				}
				binding.HostIP = addr
			}
			config.ExposedPorts[port] = struct{}{}
			host.PortBindings[port] = append(host.PortBindings[port], binding)
		}
	}
	result, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{Name: spec.Name, Config: config, HostConfig: host})
	if err != nil {
		return "", classify(err, "create container %s", spec.Name)
	}
	return result.ID, nil
}

func (e *Engine) CopyToContainer(ctx context.Context, id, dir string, archive io.Reader) error {
	if _, err := e.client.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: dir, Content: archive, CopyUIDGID: true}); err != nil {
		return classify(err, "copy into container %s at %s", id, dir)
	}
	return nil
}

func (e *Engine) StartContainer(ctx context.Context, id string) error {
	if _, err := e.client.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return classify(err, "start container %s", id)
	}
	return nil
}

func (e *Engine) StopContainer(ctx context.Context, id string, grace time.Duration) error {
	seconds := int(grace.Round(time.Second) / time.Second)
	if _, err := e.client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &seconds}); err != nil {
		return classify(err, "stop container %s", id)
	}
	return nil
}

func (e *Engine) RemoveContainer(ctx context.Context, id string) error {
	// RemoveVolumes stays false: removal never cascades to data (ADR 0005 §2).
	if _, err := e.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{}); err != nil {
		return classify(err, "remove container %s", id)
	}
	return nil
}

func (e *Engine) ExecContainer(ctx context.Context, id string, req runtime.ExecRequest) (runtime.ExecResult, error) {
	created, err := e.client.ExecCreate(ctx, id, client.ExecCreateOptions{
		User: req.User, Env: req.Env, Cmd: req.Cmd, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return runtime.ExecResult{}, classify(err, "exec in container %s", id)
	}
	attached, err := e.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return runtime.ExecResult{}, classify(err, "attach to exec in container %s", id)
	}
	defer attached.Close()
	// Without a TTY the engine multiplexes both streams; stdcopy separates
	// them as they arrive (streams are bytes, ADR 0005 §2).
	if _, err := stdcopy.StdCopy(orDiscard(req.Stdout), orDiscard(req.Stderr), attached.Reader); err != nil {
		if ctx.Err() != nil {
			return runtime.ExecResult{}, ctx.Err()
		}
		return runtime.ExecResult{}, fmt.Errorf("read exec output in container %s: %w", id, err)
	}
	inspected, err := e.client.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return runtime.ExecResult{}, classify(err, "inspect exec in container %s", id)
	}
	if inspected.Running {
		return runtime.ExecResult{}, fmt.Errorf("exec in container %s: output ended before the command", id)
	}
	return runtime.ExecResult{ExitCode: inspected.ExitCode}, nil
}

func (e *Engine) ContainerLogs(ctx context.Context, id string, tail int, w io.Writer) error {
	logs, err := e.client.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail)})
	if err != nil {
		return classify(err, "logs of container %s", id)
	}
	defer logs.Close()
	_, err = stdcopy.StdCopy(w, w, logs)
	return err
}

func (e *Engine) ListNetworks(ctx context.Context, filter runtime.LabelFilter) ([]runtime.NetworkInfo, error) {
	result, err := e.client.NetworkList(ctx, client.NetworkListOptions{Filters: labelFilters(filter)})
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	networks := make([]runtime.NetworkInfo, 0, len(result.Items))
	for _, item := range result.Items {
		networks = append(networks, runtime.NetworkInfo{ID: item.ID, Name: item.Name, Labels: item.Labels})
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	return networks, nil
}

func (e *Engine) InspectNetwork(ctx context.Context, nameOrID string) (runtime.NetworkInfo, error) {
	result, err := e.client.NetworkInspect(ctx, nameOrID, client.NetworkInspectOptions{})
	if err != nil {
		return runtime.NetworkInfo{}, classify(err, "inspect network %s", nameOrID)
	}
	n := result.Network
	return runtime.NetworkInfo{ID: n.ID, Name: n.Name, Labels: n.Labels}, nil
}

// CreateNetwork creates a bridge network. Containers on a user-defined bridge
// resolve each other by name, and are isolated from other bridges.
func (e *Engine) CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error) {
	result, err := e.client.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: labels})
	if err != nil {
		return "", classify(err, "create network %s", name)
	}
	return result.ID, nil
}

func (e *Engine) RemoveNetwork(ctx context.Context, nameOrID string) error {
	if _, err := e.client.NetworkRemove(ctx, nameOrID, client.NetworkRemoveOptions{}); err != nil {
		return classify(err, "remove network %s", nameOrID)
	}
	return nil
}

func (e *Engine) ListVolumes(ctx context.Context, filter runtime.LabelFilter) ([]runtime.VolumeInfo, error) {
	result, err := e.client.VolumeList(ctx, client.VolumeListOptions{Filters: labelFilters(filter)})
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	volumes := make([]runtime.VolumeInfo, 0, len(result.Items))
	for _, item := range result.Items {
		volumes = append(volumes, runtime.VolumeInfo{Name: item.Name, Labels: item.Labels})
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name < volumes[j].Name })
	return volumes, nil
}

func (e *Engine) InspectVolume(ctx context.Context, name string) (runtime.VolumeInfo, error) {
	result, err := e.client.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return runtime.VolumeInfo{}, classify(err, "inspect volume %s", name)
	}
	return runtime.VolumeInfo{Name: result.Volume.Name, Labels: result.Volume.Labels}, nil
}

// CreateVolume creates a local named volume. Docker silently returns an
// existing volume of the same name, so its presence is checked first: an
// existing volume is reported as a conflict, never adopted.
func (e *Engine) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	if _, err := e.InspectVolume(ctx, name); err == nil {
		return fmt.Errorf("%w: volume %s already exists", runtime.ErrConflict, name)
	} else if !errors.Is(err, runtime.ErrNotFound) {
		return err
	}
	if _, err := e.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local", Labels: labels}); err != nil {
		return classify(err, "create volume %s", name)
	}
	return nil
}

func (e *Engine) RemoveVolume(ctx context.Context, name string) error {
	if _, err := e.client.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
		return classify(err, "remove volume %s", name)
	}
	return nil
}
