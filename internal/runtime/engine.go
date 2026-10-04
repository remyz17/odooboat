package runtime

import (
	"context"
	"slices"
)

// Capability is a behavioural guarantee demonstrated by the contract suite,
// never a property inferred from the transport (ADR 0005 §3, A1.1).
type Capability string

const (
	CapExecTTY             Capability = "exec.tty"
	CapExecResize          Capability = "exec.resize"
	CapExecSignal          Capability = "exec.signal"
	CapExecCancelConfirmed Capability = "exec.cancel-confirmed"
	CapLogsFollow          Capability = "logs.follow"
	CapLogsSeparateStreams Capability = "logs.separate-streams"
	CapLogsSourceTimes     Capability = "logs.source-timestamps"
	CapLogsBoundedSince    Capability = "logs.bounded-since"
	CapLogsFollowEnds      Capability = "logs.follow-ends"
	CapLabelsContainer     Capability = "labels.container"
	CapLabelsVolume        Capability = "labels.volume"
	CapLabelsNetwork       Capability = "labels.network"
	CapLabelsFilter        Capability = "labels.filter"
	CapNetworkPrivate      Capability = "network.private"
	CapNetworkNames        Capability = "network.name-resolution"
	CapVolumePersistent    Capability = "volume.named-persistent"
	CapVolumeShared        Capability = "volume.shared"
	CapMountBind           Capability = "mount.bind"
	CapMountBindOwnership  Capability = "mount.bind.ownership"
	CapContainerExitStatus Capability = "container.exit-status"
	CapRestartPolicy       Capability = "restart.policy"
	CapImageNoImplicitPull Capability = "image.no-implicit-pull"
	CapEngineIdentity      Capability = "engine.identity"
)

// Capabilities is the set of guarantees an adapter declares for a connection.
type Capabilities []Capability

func (c Capabilities) Has(capability Capability) bool { return slices.Contains(c, capability) }

// Missing returns the required capabilities that are not declared, so an
// operation can be rejected before any mutation.
func (c Capabilities) Missing(required ...Capability) []Capability {
	var missing []Capability
	for _, capability := range required {
		if !c.Has(capability) {
			missing = append(missing, capability)
		}
	}
	return missing
}

// EngineIdentity is an opaque engine identifier compared for equality only
// (ADR 0004 A1). Source names where the value comes from.
type EngineIdentity struct {
	Source string
	Value  string
}

type EngineInfo struct {
	Identity EngineIdentity
	Version  string
	OS       string
	Arch     string
}

// Engine is a connected runtime. Primitives are added as the contract grows.
type Engine interface {
	Capabilities() Capabilities
	Info(ctx context.Context) (EngineInfo, error)
	Close() error
}

// Locator is a bound connection snapshot (ADR 0004). Exactly one of Context
// and Socket is set for Docker and Podman; Alias appears in messages only.
type Locator struct {
	Alias   string
	Kind    string
	Context string
	Socket  string
}

// Connector connects to the engine a locator designates, without fallback.
type Connector interface {
	Connect(ctx context.Context, loc Locator) (Engine, error)
}

// Connectors dispatches by connection kind.
type Connectors map[string]Connector

func (c Connectors) Connect(ctx context.Context, loc Locator) (Engine, error) {
	connector, ok := c[loc.Kind]
	if !ok {
		return nil, NewConnectionError(ErrUnsupported, loc.Alias, "",
			"runtime kind "+loc.Kind+" is not supported yet", "")
	}
	return connector.Connect(ctx, loc)
}
