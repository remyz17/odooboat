package runtimetest

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
)

// testLogs covers bounded and followed logs (ADR 0005 §2, ADR 0010 §8).
func testLogs(t *testing.T, connect Factory) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	requireCapabilities(t, engine, runtime.CapLabelsContainer, runtime.CapLabelsFilter, runtime.CapLogsFollow,
		runtime.CapLogsFollowEnds, runtime.CapLogsBoundedSince, runtime.CapLogsSeparateStreams, runtime.CapLogsSourceTimes)
	f := newFixture(ctx, t, engine)

	id := f.container("logs", "", nil, nil, "sh", "-c", "echo out1; echo err1 >&2; sleep 2; echo out2; sleep 600")
	if err := f.containers.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	logs := func(opts runtime.LogOptions) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := f.containers.ContainerLogs(ctx, id, opts, &stdout, &stderr); err != nil {
			t.Fatalf("logs %+v: %v", opts, err)
		}
		return stdout.String(), stderr.String()
	}
	// Wait for out1, take a time strictly between out1 and out2, then wait
	// for out2.
	waitFor := func(line string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if out, _ := logs(runtime.LogOptions{Tail: -1}); strings.Contains(out, line) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("log line %q never appeared", line)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitFor("out1")
	between := time.Now()
	waitFor("out2")

	t.Run("tail", func(t *testing.T) {
		out, errOut := logs(runtime.LogOptions{Tail: 1})
		if lines := strings.Count(out+errOut, "\n"); lines != 1 {
			t.Fatalf("tail 1 gave %d lines: stdout %q, stderr %q", lines, out, errOut)
		}
	})
	t.Run("separate", func(t *testing.T) {
		out, errOut := logs(runtime.LogOptions{Tail: -1})
		if out != "out1\nout2\n" || errOut != "err1\n" {
			t.Fatalf("stdout %q, stderr %q", out, errOut)
		}
	})
	t.Run("since", func(t *testing.T) {
		out, errOut := logs(runtime.LogOptions{Tail: -1, Since: between})
		if out != "out2\n" || errOut != "" {
			t.Fatalf("since: stdout %q, stderr %q", out, errOut)
		}
	})
	t.Run("timestamps", func(t *testing.T) {
		out, _ := logs(runtime.LogOptions{Tail: -1, Timestamps: true})
		stamped := regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z out\d$`)
		if got := len(stamped.FindAllString(out, -1)); got != 2 {
			t.Fatalf("timestamped lines = %d in %q", got, out)
		}
	})
	t.Run("follow cancelled", func(t *testing.T) {
		followCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			var sink bytes.Buffer
			done <- f.containers.ContainerLogs(followCtx, id, runtime.LogOptions{Tail: 0, Follow: true}, &sink, &sink)
		}()
		time.Sleep(500 * time.Millisecond)
		stop()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled follow returned %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("follow did not end after cancellation")
		}
	})
	t.Run("follow ends at stop", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			var sink bytes.Buffer
			done <- f.containers.ContainerLogs(ctx, id, runtime.LogOptions{Tail: 0, Follow: true}, &sink, &sink)
		}()
		time.Sleep(500 * time.Millisecond)
		if err := f.containers.StopContainer(ctx, id, 0); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("follow ended with %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("follow did not end when the container stopped")
		}
	})
}
