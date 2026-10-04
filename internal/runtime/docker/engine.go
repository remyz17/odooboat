package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/moby/moby/client"
	"github.com/remyz17/odooboat/internal/runtime"
)

// IdentitySource names the origin of the engine identity recorded in bindings.
const IdentitySource = "docker.info.id"

// pingTimeout bounds the availability check performed by Connect.
const pingTimeout = 5 * time.Second

// capabilities lists only the guarantees exercised by the contract suite.
var capabilities = runtime.Capabilities{runtime.CapEngineIdentity}

type Engine struct {
	alias    string
	endpoint string
	client   *client.Client
}

var _ runtime.Engine = (*Engine)(nil)

// Connect resolves the locator and performs a bounded ping. The client is
// built from the resolved endpoint only; no environment variable is read.
func Connect(ctx context.Context, loc runtime.Locator, configDir string) (*Engine, error) {
	endpoint, err := ResolveEndpoint(loc, configDir)
	if err != nil {
		return nil, err
	}
	cli, err := client.New(client.WithHost(endpoint))
	if err != nil {
		return nil, runtime.NewConnectionError(runtime.ErrUnavailable, loc.Alias, endpoint,
			fmt.Sprintf("create Docker client: %v", err), "")
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if _, err := cli.Ping(pingCtx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		_ = cli.Close()
		return nil, unavailable(loc.Alias, endpoint, err)
	}
	return &Engine{alias: loc.Alias, endpoint: endpoint, client: cli}, nil
}

// Connector connects Docker locators. An empty ConfigDir means DefaultConfigDir.
type Connector struct {
	ConfigDir string
}

var _ runtime.Connector = Connector{}

func (c Connector) Connect(ctx context.Context, loc runtime.Locator) (runtime.Engine, error) {
	configDir := c.ConfigDir
	if configDir == "" {
		dir, err := DefaultConfigDir()
		if err != nil {
			return nil, runtime.NewConnectionError(runtime.ErrUnavailable, loc.Alias, "", err.Error(), "")
		}
		configDir = dir
	}
	return Connect(ctx, loc, configDir)
}

func (e *Engine) Capabilities() runtime.Capabilities { return capabilities }

func (e *Engine) Info(ctx context.Context) (runtime.EngineInfo, error) {
	result, err := e.client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return runtime.EngineInfo{}, unavailable(e.alias, e.endpoint, err)
	}
	info := result.Info
	if info.ID == "" {
		return runtime.EngineInfo{}, runtime.NewConnectionError(runtime.ErrUnavailable, e.alias, e.endpoint,
			"Docker engine reported no identity", "")
	}
	return runtime.EngineInfo{
		Identity: runtime.EngineIdentity{Source: IdentitySource, Value: info.ID},
		Version:  info.ServerVersion,
		OS:       info.OSType,
		Arch:     info.Architecture,
	}, nil
}

func (e *Engine) Close() error { return e.client.Close() }

func unavailable(alias, endpoint string, err error) error {
	rule := fmt.Sprintf("Docker engine is not reachable: %v", err)
	if errors.Is(err, context.DeadlineExceeded) {
		rule = "Docker engine did not answer in time"
	}
	return runtime.NewConnectionError(runtime.ErrUnavailable, alias, endpoint, rule,
		"start Docker or check the connection's context or socket")
}
