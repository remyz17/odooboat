package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/envspec"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/workspace"
)

type DownRequest struct {
	Selector Selector
	Wait     WaitOptions
	Progress io.Writer
}

type DownResult struct {
	WorkspaceID   string           `yaml:"workspaceId" json:"workspaceId"`
	EnvironmentID string           `yaml:"environmentId" json:"environmentId"`
	Environment   string           `yaml:"environment" json:"environment"`
	Removed       []ResourceReport `yaml:"removed" json:"removed"`
	// Kept lists the volumes: down never removes data (ADR 0009 §7).
	Kept []ResourceReport `yaml:"kept" json:"kept"`
	// Skipped lists resources down refused to touch, with the reason.
	Skipped     []ResourceReport           `yaml:"skipped" json:"skipped"`
	Preferences config.ResolvedPreferences `yaml:"-" json:"-"`
}

// stopOrder stops Odoo before PostgreSQL, so Odoo never loses its database
// while serving.
var stopOrder = map[runtime.Role]int{runtime.RoleOdoo: 0, runtime.RoleJob: 1, runtime.RolePostgres: 2}

// Down stops and removes the environment's containers and network, keeping
// its volumes (ADR 0009 §7).
func (s WorkspaceService) Down(ctx context.Context, req DownRequest) (DownResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = io.Discard
	}
	var result DownResult
	err := s.withEnvironment(ctx, req.Selector, OperationDown, req.Wait, func(ctx context.Context, env LockedEnvironment) error {
		result.WorkspaceID, result.EnvironmentID, result.Environment, result.Preferences = env.WorkspaceID, env.Binding.ID, env.Name, env.Preferences
		stores, err := lifecycleStores(env, runtime.CapLabelsContainer, runtime.CapLabelsNetwork, runtime.CapLabelsVolume, runtime.CapLabelsFilter)
		if err != nil {
			return err
		}
		filter := runtime.LabelFilter{runtime.LabelWorkspace: env.WorkspaceID, runtime.LabelEnvironment: env.Binding.ID}
		claimOf := func(kind runtime.ResourceType, labels map[string]string) (runtime.Role, string) {
			claim := runtime.DecodeOwnership(kind, labels)
			switch {
			case claim.State == runtime.ClaimNewerSchema || claim.State == runtime.ClaimRequiresUnknown:
				return claim.Ownership.Role, "written by a newer odooboat; read-only"
			case claim.State != runtime.ClaimValid:
				return "", "odooboat labels are malformed; never mutated"
			case claim.Ownership.WorkspaceID != env.WorkspaceID || claim.Ownership.EnvironmentID != env.Binding.ID:
				return claim.Ownership.Role, "belongs to another environment"
			}
			return claim.Ownership.Role, ""
		}

		containers, err := stores.containers.ListContainers(ctx, filter)
		if err != nil {
			return err
		}
		type target struct {
			info runtime.ContainerInfo
			role runtime.Role
		}
		var targets []target
		for _, c := range containers {
			role, reason := claimOf(runtime.ResourceContainer, c.Labels)
			if reason != "" {
				result.Skipped = append(result.Skipped, ResourceReport{Kind: runtime.ResourceContainer, Role: role, Name: c.Name, Reason: reason})
				continue
			}
			targets = append(targets, target{c, role})
		}
		sort.SliceStable(targets, func(i, j int) bool { return stopOrder[targets[i].role] < stopOrder[targets[j].role] })
		for _, t := range targets {
			grace := odooStopGrace
			if t.role == runtime.RolePostgres {
				grace = postgresStopGrace
			}
			fmt.Fprintf(progress, "container %s: stopping\n", t.info.Name)
			if t.info.State == runtime.ContainerRunning {
				if err := stores.containers.StopContainer(ctx, t.info.ID, grace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
					return partial(err, result.Removed)
				}
			}
			if err := stores.containers.RemoveContainer(ctx, t.info.ID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return partial(err, result.Removed)
			}
			result.Removed = append(result.Removed, ResourceReport{Kind: runtime.ResourceContainer, Role: t.role, Name: t.info.Name, Action: "removed"})
		}

		networks, err := stores.networks.ListNetworks(ctx, filter)
		if err != nil {
			return err
		}
		for _, n := range networks {
			role, reason := claimOf(runtime.ResourceNetwork, n.Labels)
			if reason != "" {
				result.Skipped = append(result.Skipped, ResourceReport{Kind: runtime.ResourceNetwork, Role: role, Name: n.Name, Reason: reason})
				continue
			}
			fmt.Fprintf(progress, "network %s: removing\n", n.Name)
			if err := stores.networks.RemoveNetwork(ctx, n.ID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return partial(err, result.Removed)
			}
			result.Removed = append(result.Removed, ResourceReport{Kind: runtime.ResourceNetwork, Role: role, Name: n.Name, Action: "removed"})
		}

		volumes, err := stores.volumes.ListVolumes(ctx, filter)
		if err != nil {
			return err
		}
		for _, v := range volumes {
			role, _ := claimOf(runtime.ResourceVolume, v.Labels)
			result.Kept = append(result.Kept, ResourceReport{Kind: runtime.ResourceVolume, Role: role, Name: v.Name, Action: "kept"})
		}
		return nil
	})
	return result, err
}

type StatusRequest struct{ Selector Selector }

type StatusResult struct {
	Environment   string `yaml:"environment" json:"environment"`
	Bound         bool   `yaml:"bound" json:"bound"`
	EnvironmentID string `yaml:"environmentId,omitempty" json:"environmentId,omitempty"`
	// Running is true when both containers run.
	Running   bool             `yaml:"running" json:"running"`
	URL       string           `yaml:"url,omitempty" json:"url,omitempty"`
	Resources []ResourceReport `yaml:"resources" json:"resources"`
	// Orphans are resources of this workspace whose environment is not in
	// local state.
	Orphans     []ResourceReport           `yaml:"orphans" json:"orphans"`
	Warnings    []string                   `yaml:"warnings" json:"warnings"`
	Preferences config.ResolvedPreferences `yaml:"-" json:"-"`
}

// Status observes the environment's resources without the lock and without
// changing anything (ADR 0009 §8).
func (s WorkspaceService) Status(ctx context.Context, req StatusRequest) (StatusResult, error) {
	resolved, err := config.Resolve(req.Selector.options())
	if err != nil {
		return StatusResult{}, err
	}
	cfg := resolved.Config
	result := StatusResult{Environment: cfg.EnvironmentName, Preferences: cfg.Preferences, Resources: []ResourceReport{}, Orphans: []ResourceReport{}}
	if cfg.Odoo.MasterPassword.IsZero() {
		result.Warnings = append(result.Warnings, "no masterPassword is configured: Odoo's database manager uses its default master password")
	}
	location, err := workspace.Locate(resolved.Sources.ProjectPath)
	if err != nil {
		return result, err
	}
	inspection, err := workspace.Inspect(s.store, location)
	if err != nil {
		return result, err
	}
	if inspection.State == nil {
		return result, nil
	}
	binding, ok := inspection.State.Environments[cfg.EnvironmentName]
	if !ok {
		return result, nil
	}
	result.Bound, result.EnvironmentID = true, binding.ID

	engine, err := s.runtimes.Connect(ctx, runtime.Locator{Alias: binding.Connection.Alias, Kind: binding.Connection.Kind,
		Context: binding.Connection.Context, Socket: binding.Connection.Socket})
	if err != nil {
		return result, err
	}
	defer engine.Close()
	info, err := engine.Info(ctx)
	if err != nil {
		return result, err
	}
	// Resources listed on another engine would be misleading: the recorded
	// identity must match, as for any operation (ADR 0004 A1).
	if recorded := binding.Engine; recorded != nil && !recorded.Unavailable && engine.Capabilities().Has(runtime.CapEngineIdentity) &&
		(recorded.Source != info.Identity.Source || recorded.Value != info.Identity.Value) {
		return result, fmt.Errorf("%w: environment %q was bound to engine %s=%s but the connection reaches %s=%s",
			workspace.ErrBindingConflict, cfg.EnvironmentName, recorded.Source, recorded.Value, info.Identity.Source, info.Identity.Value)
	}
	stores, err := lifecycleStores(LockedEnvironment{Engine: engine, Binding: binding}, runtime.CapLabelsFilter)
	if err != nil {
		return result, err
	}

	workspaceID := inspection.State.Workspace.ID
	known := map[string]bool{}
	for _, b := range inspection.State.Environments {
		known[b.ID] = true
	}
	running := map[runtime.Role]bool{}
	report := func(kind runtime.ResourceType, name, state string, labels map[string]string) {
		claim := runtime.DecodeOwnership(kind, labels)
		r := ResourceReport{Kind: kind, Role: claim.Ownership.Role, Name: name, State: state}
		if claim.State != runtime.ClaimValid {
			r.Reason = "claim " + string(claim.State)
		}
		switch {
		case claim.Ownership.EnvironmentID == binding.ID:
			result.Resources = append(result.Resources, r)
		case !known[claim.Ownership.EnvironmentID]:
			result.Orphans = append(result.Orphans, r)
		}
	}
	filter := runtime.LabelFilter{runtime.LabelWorkspace: workspaceID}
	containers, err := stores.containers.ListContainers(ctx, filter)
	if err != nil {
		return result, err
	}
	for _, c := range containers {
		state := string(c.State)
		if c.State == runtime.ContainerExited {
			state += " (" + strconv.Itoa(c.ExitCode) + ")"
		}
		report(runtime.ResourceContainer, c.Name, state, c.Labels)
		claim := runtime.DecodeOwnership(runtime.ResourceContainer, c.Labels)
		if claim.State == runtime.ClaimValid && claim.Ownership.EnvironmentID == binding.ID && c.State == runtime.ContainerRunning {
			running[claim.Ownership.Role] = true
			if claim.Ownership.Role == runtime.RoleOdoo {
				for _, p := range c.Ports {
					if p.ContainerPort == envspec.HTTPPort {
						result.URL = "http://" + envspec.HostAddress + ":" + strconv.Itoa(p.HostPort)
					}
				}
			}
		}
	}
	networks, err := stores.networks.ListNetworks(ctx, filter)
	if err != nil {
		return result, err
	}
	for _, n := range networks {
		report(runtime.ResourceNetwork, n.Name, "", n.Labels)
	}
	volumes, err := stores.volumes.ListVolumes(ctx, filter)
	if err != nil {
		return result, err
	}
	for _, v := range volumes {
		report(runtime.ResourceVolume, v.Name, "", v.Labels)
	}
	result.Running = running[runtime.RoleOdoo] && running[runtime.RolePostgres]
	return result, nil
}
