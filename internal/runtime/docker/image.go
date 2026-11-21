package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"github.com/remyz17/odooboat/internal/runtime"
)

var (
	_ runtime.ImageStore   = (*Engine)(nil)
	_ runtime.ImageBuilder = (*Engine)(nil)
)

// cancelTimeout bounds the BuildCancel request sent after a cancelled build.
const cancelTimeout = 5 * time.Second

func (e *Engine) InspectImage(ctx context.Context, ref string) (runtime.ImageInfo, error) {
	result, err := e.client.ImageInspect(ctx, ref)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return runtime.ImageInfo{}, fmt.Errorf("%w: %s", runtime.ErrImageNotFound, ref)
		}
		return runtime.ImageInfo{}, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	info := runtime.ImageInfo{ID: result.ID, RepoDigests: result.RepoDigests, OS: result.Os, Arch: result.Architecture}
	if result.Config != nil {
		info.Labels, info.Env = result.Config.Labels, result.Config.Env
	}
	return info, nil
}

func (e *Engine) PullImage(ctx context.Context, ref string, progress io.Writer) error {
	if !runtime.IsPinned(ref) {
		return fmt.Errorf("refusing to pull %s: only references pinned by digest are pulled", ref)
	}
	progress = orDiscard(progress)
	response, err := e.client.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer response.Close()
	for message, err := range response.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if message.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, message.Error.Message)
		}
		// Byte-level progress is noise in a log; layer milestones are kept.
		if message.Status != "" && message.Progress == nil {
			if message.ID != "" {
				fmt.Fprintf(progress, "%s: %s\n", message.ID, message.Status)
			} else {
				fmt.Fprintln(progress, message.Status)
			}
		}
	}
	return nil
}

func (e *Engine) TagImage(ctx context.Context, source, target string) error {
	if _, err := e.client.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: target}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("%w: %s", runtime.ErrImageNotFound, source)
		}
		return fmt.Errorf("tag %s as %s: %w", source, target, err)
	}
	return nil
}

// BuildImage builds with BuildKit through the engine API, without a session:
// the context travels as the request body (spike docker-build-sdk). BuildKit
// pulls a missing FROM, so callers inspect bases first.
func (e *Engine) BuildImage(ctx context.Context, req runtime.BuildRequest) (runtime.BuiltImage, error) {
	identity, err := e.Info(ctx)
	if err != nil {
		return runtime.BuiltImage{}, err
	}
	buildID, err := newBuildID()
	if err != nil {
		return runtime.BuiltImage{}, err
	}
	result, err := e.client.ImageBuild(ctx, req.Context, client.ImageBuildOptions{
		Version: build.BuilderBuildKit, Tags: req.Tags, Labels: req.Labels, Remove: true, BuildID: buildID,
	})
	if err != nil {
		return runtime.BuiltImage{}, fmt.Errorf("%w: %v", runtime.ErrImageBuild, err)
	}
	defer result.Body.Close()
	id, err := readBuild(result.Body, newTraceRenderer(orDiscard(req.Progress)))
	if ctx.Err() != nil {
		// Closing the stream stops the build; the explicit cancel makes it so
		// even if the engine kept the request alive.
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
		defer cancel()
		_, _ = e.client.BuildCancel(cancelCtx, buildID, client.BuildCancelOptions{})
		return runtime.BuiltImage{}, ctx.Err()
	}
	if err != nil {
		return runtime.BuiltImage{}, err
	}
	return runtime.BuiltImage{ID: id, Engine: identity.Identity}, nil
}

// traceID marks BuildKit progress messages in the build stream.
const traceID = "moby.buildkit.trace"

// readBuild consumes the build stream: BuildKit progress is rendered, the
// image ID is taken from the final aux message, and the first error is kept.
func readBuild(body io.Reader, trace *traceRenderer) (string, error) {
	decoder := json.NewDecoder(body)
	var id string
	for {
		var message jsonstream.Message
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("%w: read build output: %v", runtime.ErrImageBuild, err)
		}
		switch {
		case message.Error != nil:
			return "", fmt.Errorf("%w: %s", runtime.ErrImageBuild, message.Error.Message)
		case message.Aux != nil && message.ID == traceID:
			var payload []byte
			if err := json.Unmarshal(*message.Aux, &payload); err == nil {
				trace.render(payload)
			}
		case message.Aux != nil:
			var result struct{ ID string }
			if err := json.Unmarshal(*message.Aux, &result); err == nil && result.ID != "" {
				id = result.ID
			}
		case message.Stream != "":
			trace.text(message.Stream)
		}
	}
	if id == "" {
		return "", fmt.Errorf("%w: the engine reported no image ID", runtime.ErrImageBuild)
	}
	return id, nil
}

func newBuildID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "odooboat-" + hex.EncodeToString(b[:]), nil
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
