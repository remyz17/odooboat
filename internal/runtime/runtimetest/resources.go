package runtimetest

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/workspace"
)

// stores returns the resource primitive sets, failing when the adapter does
// not implement them.
func stores(t *testing.T, engine runtime.Engine) (runtime.ContainerStore, runtime.NetworkStore, runtime.VolumeStore) {
	t.Helper()
	containers, ok := engine.(runtime.ContainerStore)
	if !ok {
		t.Fatal("adapter does not implement runtime.ContainerStore")
	}
	networks, ok := engine.(runtime.NetworkStore)
	if !ok {
		t.Fatal("adapter does not implement runtime.NetworkStore")
	}
	volumes, ok := engine.(runtime.VolumeStore)
	if !ok {
		t.Fatal("adapter does not implement runtime.VolumeStore")
	}
	return containers, networks, volumes
}

// fixture holds one test's resources. They carry ownership labels of a
// workspace dedicated to the run and are removed by label afterwards (ADR
// 0005 §8), never by name.
type fixture struct {
	t          *testing.T
	ctx        context.Context
	image      string
	workspace  string
	env        string
	operation  string
	containers runtime.ContainerStore
	networks   runtime.NetworkStore
	volumes    runtime.VolumeStore
}

func newFixture(ctx context.Context, t *testing.T, engine runtime.Engine) *fixture {
	t.Helper()
	ref := os.Getenv(PullRefEnv)
	if ref == "" {
		t.Fatalf("%s is empty: the container tests need an image with a shell, pinned by digest "+
			"(for example docker.io/library/alpine:3.22@sha256:...)", PullRefEnv)
	}
	images := imageStore(t, engine)
	if _, err := images.InspectImage(ctx, ref); errors.Is(err, runtime.ErrImageNotFound) {
		if err := images.PullImage(ctx, ref, nil); err != nil {
			t.Fatalf("pull %s: %v", ref, err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 3)
	for i := range ids {
		id, err := workspace.NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	containers, networks, volumes := stores(t, engine)
	f := &fixture{t: t, ctx: ctx, image: ref, workspace: ids[0], env: ids[1], operation: ids[2],
		containers: containers, networks: networks, volumes: volumes}
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) labels(kind runtime.ResourceType, role runtime.Role) map[string]string {
	f.t.Helper()
	ownership := runtime.Ownership{Scope: runtime.ScopeEnvironment, WorkspaceID: f.workspace, EnvironmentID: f.env,
		Role: role, OperationID: f.operation, CreatedBy: "odooboat/contract",
		DisplayProject: "contract", DisplayEnvironment: "test"}
	if kind == runtime.ResourceContainer {
		ownership.Spec = "sha256:" + strings.Repeat("0", 64)
	}
	labels, err := ownership.Labels(kind)
	if err != nil {
		f.t.Fatal(err)
	}
	return labels
}

func (f *fixture) filter() runtime.LabelFilter {
	return runtime.LabelFilter{runtime.LabelWorkspace: f.workspace}
}

func (f *fixture) name(role string) string {
	return "ob-contract-" + role + "-" + f.workspace[:8]
}

func (f *fixture) container(role string, network string, mounts []runtime.Mount, ports []runtime.PortBinding, cmd ...string) string {
	f.t.Helper()
	id, err := f.containers.CreateContainer(f.ctx, runtime.ContainerSpec{
		Name: f.name(role), Image: f.image, Labels: f.labels(runtime.ResourceContainer, runtime.RoleJob),
		Cmd: cmd, Network: network, Mounts: mounts, Ports: ports,
	})
	if err != nil {
		f.t.Fatalf("create %s: %v", role, err)
	}
	return id
}

func (f *fixture) exec(id string, cmd ...string) (runtime.ExecResult, string, string) {
	f.t.Helper()
	var stdout, stderr bytes.Buffer
	result, err := f.containers.ExecContainer(f.ctx, id, runtime.ExecRequest{Cmd: cmd, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		f.t.Fatalf("exec %v: %v", cmd, err)
	}
	return result, stdout.String(), stderr.String()
}

func (f *fixture) waitState(id string, want runtime.ContainerState) runtime.ContainerInfo {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := f.containers.InspectContainer(f.ctx, id)
		if err != nil {
			f.t.Fatal(err)
		}
		if info.State == want {
			return info
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("container %s is %s, want %s", id, info.State, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *fixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if listed, err := f.containers.ListContainers(ctx, f.filter()); err == nil {
		for _, c := range listed {
			_ = f.containers.StopContainer(ctx, c.ID, 0)
			_ = f.containers.RemoveContainer(ctx, c.ID)
		}
	}
	if listed, err := f.networks.ListNetworks(ctx, f.filter()); err == nil {
		for _, n := range listed {
			_ = f.networks.RemoveNetwork(ctx, n.ID)
		}
	}
	if listed, err := f.volumes.ListVolumes(ctx, f.filter()); err == nil {
		for _, v := range listed {
			_ = f.volumes.RemoveVolume(ctx, v.Name)
		}
	}
}

func fileArchive(t *testing.T, name, content string, mode int64) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func requireCapabilities(t *testing.T, engine runtime.Engine, caps ...runtime.Capability) {
	t.Helper()
	for _, c := range caps {
		if !engine.Capabilities().Has(c) {
			t.Fatalf("adapter does not declare %s, which this test establishes", c)
		}
	}
}

// Containers: labelled lifecycle, filtered listing, exec results and stream
// separation, copying before start, exit status, and bounded logs.
func testContainerLifecycle(t *testing.T, connect Factory) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	requireCapabilities(t, engine, runtime.CapLabelsContainer, runtime.CapLabelsFilter, runtime.CapContainerCopy,
		runtime.CapContainerExitStatus, runtime.CapImageNoImplicitPull)
	f := newFixture(ctx, t, engine)

	id := f.container("main", "", nil, nil, "sleep", "300")
	if _, err := f.containers.CreateContainer(ctx, runtime.ContainerSpec{Name: f.name("main"), Image: f.image}); !errors.Is(err, runtime.ErrConflict) {
		t.Fatalf("duplicate name: err = %v, want ErrConflict", err)
	}
	if _, err := f.containers.CreateContainer(ctx, runtime.ContainerSpec{Name: f.name("absent"), Image: "odooboat-contract/absent:none"}); !errors.Is(err, runtime.ErrImageNotFound) {
		t.Fatalf("absent image: err = %v, want ErrImageNotFound", err)
	}

	created, err := f.containers.InspectContainer(ctx, f.name("main"))
	if err != nil || created.ID != id || created.State != runtime.ContainerCreated {
		t.Fatalf("inspect by name = %+v, %v", created, err)
	}
	claim := runtime.DecodeOwnership(runtime.ResourceContainer, created.Labels)
	if claim.State != runtime.ClaimValid || claim.Ownership.WorkspaceID != f.workspace {
		t.Fatalf("labels did not round-trip: %+v", claim)
	}

	// A file copied before the start is visible to the process.
	if err := f.containers.CopyToContainer(ctx, id, "/tmp", fileArchive(t, "copied.conf", "copied\n", 0o640)); err != nil {
		t.Fatal(err)
	}
	if err := f.containers.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.waitState(id, runtime.ContainerRunning)
	if result, out, _ := f.exec(id, "cat", "/tmp/copied.conf"); result.ExitCode != 0 || out != "copied\n" {
		t.Fatalf("copied file = %q (exit %d)", out, result.ExitCode)
	}

	// A command exiting non-zero is a result; both streams stay separate.
	result, out, errOut := f.exec(id, "sh", "-c", "echo out; echo err >&2; exit 3")
	if result.ExitCode != 3 || out != "out\n" || errOut != "err\n" {
		t.Fatalf("exec = exit %d, stdout %q, stderr %q", result.ExitCode, out, errOut)
	}

	listed, err := f.containers.ListContainers(ctx, f.filter())
	if err != nil || len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("list by label = %+v, %v", listed, err)
	}
	if other, err := f.containers.ListContainers(ctx, runtime.LabelFilter{runtime.LabelWorkspace: f.env}); err != nil || len(other) != 0 {
		t.Fatalf("list by another label = %+v, %v", other, err)
	}

	if err := f.containers.StopContainer(ctx, id, time.Second); err != nil {
		t.Fatal(err)
	}
	f.waitState(id, runtime.ContainerExited)
	if err := f.containers.RemoveContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.containers.InspectContainer(ctx, id); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("removed container: err = %v, want ErrNotFound", err)
	}

	// The exit status of the main process is observable after it exits.
	exits := f.container("exits", "", nil, nil, "sh", "-c", "echo last line; exit 7")
	if err := f.containers.StartContainer(ctx, exits); err != nil {
		t.Fatal(err)
	}
	if info := f.waitState(exits, runtime.ContainerExited); info.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", info.ExitCode)
	}
	var logs bytes.Buffer
	if err := f.containers.ContainerLogs(ctx, exits, runtime.LogOptions{Tail: 5}, &logs, &logs); err != nil || !strings.Contains(logs.String(), "last line") {
		t.Fatalf("logs = %q, %v", logs.String(), err)
	}
}

// Networks and volumes: name resolution on a private network, port
// publication, persistence across replacement, and removal that never
// cascades to data.
func testNetworkAndVolume(t *testing.T, connect Factory) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	requireCapabilities(t, engine, runtime.CapLabelsNetwork, runtime.CapLabelsVolume, runtime.CapNetworkPrivate,
		runtime.CapNetworkNames, runtime.CapVolumePersistent, runtime.CapPortPublish, runtime.CapMountBind)
	f := newFixture(ctx, t, engine)

	network := f.name("net")
	if _, err := f.networks.CreateNetwork(ctx, network, f.labels(runtime.ResourceNetwork, runtime.RoleEnvironment)); err != nil {
		t.Fatal(err)
	}
	if listed, err := f.networks.ListNetworks(ctx, f.filter()); err != nil || len(listed) != 1 || listed[0].Name != network {
		t.Fatalf("networks = %+v, %v", listed, err)
	}
	volume := f.name("data")
	if err := f.volumes.CreateVolume(ctx, volume, f.labels(runtime.ResourceVolume, runtime.RoleOdooData)); err != nil {
		t.Fatal(err)
	}
	if err := f.volumes.CreateVolume(ctx, volume, f.labels(runtime.ResourceVolume, runtime.RoleOdooData)); !errors.Is(err, runtime.ErrConflict) {
		t.Fatalf("existing volume: err = %v, want ErrConflict", err)
	}
	if info, err := f.volumes.InspectVolume(ctx, volume); err != nil ||
		runtime.DecodeOwnership(runtime.ResourceVolume, info.Labels).State != runtime.ClaimValid {
		t.Fatalf("volume = %+v, %v", info, err)
	}

	data := []runtime.Mount{{Kind: runtime.MountVolume, Source: volume, Target: "/data"}}
	server := f.container("server", network, data, []runtime.PortBinding{{ContainerPort: 8080, HostIP: "127.0.0.1"}},
		"sh", "-c", "echo kept > /data/marker; while true; do echo hello | nc -l -p 8080; done")
	if err := f.containers.StartContainer(ctx, server); err != nil {
		t.Fatal(err)
	}
	info := f.waitState(server, runtime.ContainerRunning)
	if len(info.Ports) != 1 || info.Ports[0].ContainerPort != 8080 || info.Ports[0].HostIP != "127.0.0.1" || info.Ports[0].HostPort == 0 {
		t.Fatalf("published ports = %+v", info.Ports)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(info.Ports[0].HostPort))
	if err := dialEventually(ctx, address); err != nil {
		t.Fatalf("published port %s: %v", address, err)
	}

	// Another container of the network reaches the server by name.
	client := f.container("client", network, nil, nil, "sleep", "300")
	if err := f.containers.StartContainer(ctx, client); err != nil {
		t.Fatal(err)
	}
	f.waitState(client, runtime.ContainerRunning)
	if result, out, errOut := f.exec(client, "sh", "-c", "nc -w 5 "+f.name("server")+" 8080 </dev/null"); result.ExitCode != 0 || !strings.Contains(out, "hello") {
		t.Fatalf("by name: exit %d, stdout %q, stderr %q", result.ExitCode, out, errOut)
	}

	// Data written by one container survives its replacement.
	for _, id := range []string{server, client} {
		if err := f.containers.StopContainer(ctx, id, 0); err != nil {
			t.Fatal(err)
		}
		if err := f.containers.RemoveContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.volumes.InspectVolume(ctx, volume); err != nil {
		t.Fatalf("removing a container removed its volume: %v", err)
	}
	host := t.TempDir()
	if err := os.WriteFile(host+"/source.txt", []byte("from host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts := append(data, runtime.Mount{Kind: runtime.MountBind, Source: host, Target: "/src", ReadOnly: true})
	reader := f.container("reader", "", mounts, nil, "sleep", "300")
	if err := f.containers.StartContainer(ctx, reader); err != nil {
		t.Fatal(err)
	}
	f.waitState(reader, runtime.ContainerRunning)
	if result, out, _ := f.exec(reader, "cat", "/data/marker"); result.ExitCode != 0 || out != "kept\n" {
		t.Fatalf("volume content after replacement = %q (exit %d)", out, result.ExitCode)
	}
	// A read-only bind mount shows the host tree and refuses writes.
	if result, out, _ := f.exec(reader, "cat", "/src/source.txt"); result.ExitCode != 0 || out != "from host\n" {
		t.Fatalf("bind mount content = %q (exit %d)", out, result.ExitCode)
	}
	if result, _, _ := f.exec(reader, "sh", "-c", "echo x > /src/written"); result.ExitCode == 0 {
		t.Fatal("a read-only bind mount accepted a write")
	}
}

func dialEventually(ctx context.Context, address string) error {
	var last error
	for i := 0; i < 50; i++ {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err == nil {
			buf := make([]byte, 5)
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _ := conn.Read(buf)
			conn.Close()
			if string(buf[:n]) == "hello" {
				return nil
			}
			last = fmt.Errorf("read %q", buf[:n])
		} else {
			last = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

// largeContextSize exceeds the socket buffers between client and engine,
// where a streamed build context used to stall (spike docker-build-sdk).
const largeContextSize = 96 << 20

// A streamed build context larger than the transport's buffers completes.
func testBuildLargeContext(t *testing.T, connect Factory, hooks Hooks) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	builder, ok := engine.(runtime.ImageBuilder)
	if !ok || !engine.Capabilities().Has(runtime.CapImageBuild) {
		t.Log("image.build is not declared; nothing to verify")
		return
	}
	reader, writer := io.Pipe()
	go func() {
		tw := tar.NewWriter(writer)
		dockerfile := "FROM scratch\nCOPY large /large\n"
		err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile)), Typeflag: tar.TypeReg})
		if err == nil {
			_, err = tw.Write([]byte(dockerfile))
		}
		if err == nil {
			err = tw.WriteHeader(&tar.Header{Name: "large", Mode: 0o644, Size: largeContextSize, Typeflag: tar.TypeReg})
		}
		chunk := make([]byte, 1<<20)
		for written := 0; err == nil && written < largeContextSize; written += len(chunk) {
			_, err = tw.Write(chunk)
		}
		if err == nil {
			err = tw.Close()
		}
		writer.CloseWithError(err)
	}()
	tag := uniqueName(t, "odooboat-contract/large")
	cleanup(t, hooks, tag)
	if _, err := builder.BuildImage(ctx, runtime.BuildRequest{Context: reader, Tags: []string{tag}}); err != nil {
		t.Fatalf("build with a %d MB context: %v", largeContextSize>>20, err)
	}
}
