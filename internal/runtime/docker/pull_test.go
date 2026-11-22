package docker

import (
	"testing"

	"github.com/moby/moby/api/types/jsonstream"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/runtime/runtimetest"
)

const layerID = "a1b2c3d4e5f6"

func collect(reports *[]runtime.Progress) runtime.ProgressFunc {
	return func(p runtime.Progress) { *reports = append(*reports, p) }
}

func states(reports []runtime.Progress) []runtime.ProgressState {
	out := make([]runtime.ProgressState, len(reports))
	for i, r := range reports {
		out[i] = r.State
	}
	return out
}

func assertStates(t *testing.T, reports []runtime.Progress, want ...runtime.ProgressState) {
	t.Helper()
	got := states(reports)
	if len(got) != len(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("states = %v, want %v", got, want)
		}
	}
}

func TestPullDecoderLayerLifecycle(t *testing.T) {
	var reports []runtime.Progress
	d := newPullDecoder(collect(&reports))
	d.message(jsonstream.Message{ID: layerID, Status: "Pulling fs layer"})
	d.message(jsonstream.Message{ID: layerID, Status: "Downloading", Progress: &jsonstream.Progress{Current: 10, Total: 100}})
	d.message(jsonstream.Message{ID: layerID, Status: "Download complete"})
	d.message(jsonstream.Message{ID: layerID, Status: "Pull complete"})
	d.close(false)

	assertStates(t, reports, runtime.ProgressStarted, runtime.ProgressRunning, runtime.ProgressRunning, runtime.ProgressDone)
	for _, r := range reports {
		if r.Kind != runtime.ProgressLayer || r.ID != layerID || r.Name != layerID {
			t.Fatalf("report = %+v, want a layer %s", r, layerID)
		}
	}
	if r := reports[1]; r.Current != 10 || r.Total != 100 {
		t.Fatalf("downloading = %+v, want 10/100", r)
	}
	runtimetest.CheckProgress(t, reports)
}

func TestPullDecoderAlreadyExists(t *testing.T) {
	var reports []runtime.Progress
	d := newPullDecoder(collect(&reports))
	d.message(jsonstream.Message{ID: layerID, Status: "Already exists"})
	d.close(false)
	assertStates(t, reports, runtime.ProgressStarted, runtime.ProgressCached)
	runtimetest.CheckProgress(t, reports)
}

func TestPullDecoderIgnoresNonLayerIDs(t *testing.T) {
	var reports []runtime.Progress
	d := newPullDecoder(collect(&reports))
	d.message(jsonstream.Message{ID: "16-bookworm", Status: "Pulling from library/postgres"})
	d.message(jsonstream.Message{Status: "Digest: sha256:0123"})
	d.message(jsonstream.Message{Status: "Status: Downloaded newer image for postgres:16"})
	d.close(false)
	if len(reports) != 0 {
		t.Fatalf("reports = %+v, want none", reports)
	}
}

func TestPullDecoderClosesOpenLayers(t *testing.T) {
	for _, failed := range []bool{true, false} {
		var reports []runtime.Progress
		d := newPullDecoder(collect(&reports))
		d.message(jsonstream.Message{ID: layerID, Status: "Pulling fs layer"})
		d.message(jsonstream.Message{ID: layerID, Status: "Extracting", Progress: &jsonstream.Progress{Current: 5, Total: 50}})
		d.close(failed)

		want := runtime.ProgressDone
		if failed {
			want = runtime.ProgressFailed
		}
		assertStates(t, reports, runtime.ProgressStarted, runtime.ProgressRunning, want)
		if r := reports[1]; r.Current != 0 || r.Total != 0 {
			t.Fatalf("extracting = %+v, want no byte counts", r)
		}
		runtimetest.CheckProgress(t, reports)
	}
}
