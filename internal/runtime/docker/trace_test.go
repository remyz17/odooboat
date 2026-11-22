package docker

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/runtime/runtimetest"
)

func pbBytes(num int, value []byte) []byte {
	b := binary.AppendUvarint(nil, uint64(num<<3|wireBytes))
	b = binary.AppendUvarint(b, uint64(len(value)))
	return append(b, value...)
}

func pbVarint(num int, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(num<<3|wireVarint)), value)
}

type vertexSpec struct {
	digest, name, err         string
	cached, started, complete bool
}

func vertexPayload(v vertexSpec) []byte {
	var b []byte
	b = append(b, pbBytes(1, []byte(v.digest))...)
	b = append(b, pbBytes(2, []byte("sha256:input"))...) // repeated inputs, ignored
	b = append(b, pbBytes(3, []byte(v.name))...)
	if v.cached {
		b = append(b, pbVarint(4, 1)...)
	}
	if v.started {
		b = append(b, pbBytes(5, pbVarint(1, 1700000000))...)
	}
	if v.complete {
		b = append(b, pbBytes(6, pbVarint(1, 1700000001))...)
	}
	if v.err != "" {
		b = append(b, pbBytes(7, []byte(v.err))...)
	}
	return pbBytes(1, b)
}

func logPayload(digest, msg string) []byte {
	b := append(pbBytes(1, []byte(digest)), pbVarint(3, 1)...)
	return pbBytes(3, append(b, pbBytes(4, []byte(msg))...))
}

func traceLine(t *testing.T, payload []byte) string {
	t.Helper()
	aux, err := json.Marshal(payload) // []byte marshals as base64, like the engine
	if err != nil {
		t.Fatal(err)
	}
	return `{"id":"moby.buildkit.trace","aux":` + string(aux) + "}\n"
}

func decode(payloads ...[]byte) []runtime.Progress {
	var reports []runtime.Progress
	d := newTraceDecoder(collect(&reports))
	for _, p := range payloads {
		d.payload(p)
	}
	return reports
}

func TestTraceDecoderVertexLifecycle(t *testing.T) {
	reports := decode(
		vertexPayload(vertexSpec{digest: "sha256:a", name: "[1/2] RUN make", started: true}),
		vertexPayload(vertexSpec{digest: "sha256:a", name: "[1/2] RUN make", started: true, complete: true}),
	)
	assertStates(t, reports, runtime.ProgressStarted, runtime.ProgressDone)
	for _, r := range reports {
		if r.Kind != runtime.ProgressVertex || r.ID != "sha256:a" || r.Name != "[1/2] RUN make" {
			t.Fatalf("report = %+v", r)
		}
	}
	runtimetest.CheckProgress(t, reports)
}

func TestTraceDecoderCachedAndFailed(t *testing.T) {
	cached := decode(vertexPayload(vertexSpec{digest: "sha256:a", name: "FROM base", started: true, cached: true, complete: true}))
	assertStates(t, cached, runtime.ProgressStarted, runtime.ProgressCached)
	runtimetest.CheckProgress(t, cached)

	failed := decode(vertexPayload(vertexSpec{digest: "sha256:c", name: "RUN false", started: true, complete: true, err: "exit code: 1"}))
	assertStates(t, failed, runtime.ProgressStarted, runtime.ProgressFailed)
	runtimetest.CheckProgress(t, failed)
}

func TestTraceDecoderLogs(t *testing.T) {
	var reports []runtime.Progress
	d := newTraceDecoder(collect(&reports))
	d.payload(vertexPayload(vertexSpec{digest: "sha256:b", name: "RUN make", started: true}))
	d.payload(logPayload("sha256:b", "line one\nline two\n"))
	d.payload(logPayload("sha256:unknown", "early\n"))
	d.close(false)

	if r := reports[1]; r.State != runtime.ProgressRunning || r.ID != "sha256:b" || string(r.Output) != "line one\nline two\n" {
		t.Fatalf("log report = %+v", r)
	}
	if r := reports[2]; r.State != runtime.ProgressStarted || r.ID != "sha256:unknown" {
		t.Fatalf("unknown vertex = %+v, want started first", r)
	}
	if r := reports[3]; r.State != runtime.ProgressRunning || string(r.Output) != "early\n" {
		t.Fatalf("unknown vertex log = %+v", r)
	}
	runtimetest.CheckProgress(t, reports)
}

func TestTraceDecoderLogsBeforeCompletionInOnePayload(t *testing.T) {
	// BuildKit sends a vertex's last logs in the same status as its completion.
	reports := decode(
		vertexPayload(vertexSpec{digest: "sha256:c", name: "RUN false", started: true}),
		append(vertexPayload(vertexSpec{digest: "sha256:c", name: "RUN false", started: true, complete: true, err: "exit code: 1"}),
			logPayload("sha256:c", "boom\n")...),
	)
	assertStates(t, reports, runtime.ProgressStarted, runtime.ProgressRunning, runtime.ProgressFailed)
	runtimetest.CheckProgress(t, reports)
}

func TestTraceDecoderText(t *testing.T) {
	var reports []runtime.Progress
	d := newTraceDecoder(collect(&reports))
	d.text("Step 1/2\n")
	if len(reports) != 1 || reports[0].ID != "" || string(reports[0].Output) != "Step 1/2\n" {
		t.Fatalf("reports = %+v", reports)
	}
	runtimetest.CheckProgress(t, reports)
}

func TestTraceDecoderCloseFailsOpenVertices(t *testing.T) {
	var reports []runtime.Progress
	d := newTraceDecoder(collect(&reports))
	d.payload(vertexPayload(vertexSpec{digest: "sha256:a", name: "RUN sleep", started: true}))
	d.payload(vertexPayload(vertexSpec{digest: "sha256:b", name: "COPY", started: true, complete: true}))
	d.close(true)
	assertStates(t, reports, runtime.ProgressStarted, runtime.ProgressStarted, runtime.ProgressDone, runtime.ProgressFailed)
	runtimetest.CheckProgress(t, reports)
}

func TestReadBuildDecodesTraceAndID(t *testing.T) {
	stream := traceLine(t, vertexPayload(vertexSpec{digest: "sha256:a", name: "FROM base", started: true, cached: true, complete: true})) +
		traceLine(t, []byte{0xff, 0xff}) + // undecodable payloads are ignored
		`{"aux":{"ID":"sha256:built"}}` + "\n"
	var reports []runtime.Progress
	id, err := readBuild(strings.NewReader(stream), newTraceDecoder(collect(&reports)))
	if err != nil || id != "sha256:built" {
		t.Fatalf("id = %q, %v", id, err)
	}
	assertStates(t, reports, runtime.ProgressStarted, runtime.ProgressCached)
}

func TestReadBuildReportsFailures(t *testing.T) {
	failing := traceLine(t, vertexPayload(vertexSpec{digest: "sha256:c", name: "RUN false", started: true, complete: true, err: "exit code: 1"})) +
		`{"errorDetail":{"message":"process did not complete successfully: exit code: 1"},"error":"process did not complete successfully: exit code: 1"}` + "\n"
	trace := newTraceDecoder(nil)
	_, err := readBuild(strings.NewReader(failing), trace)
	if !errors.Is(err, runtime.ErrImageBuild) || !strings.Contains(err.Error(), "exit code: 1") {
		t.Fatalf("err = %v", err)
	}
	if _, err := readBuild(strings.NewReader(`{"stream":"Step 1/1"}`+"\n"), trace); !errors.Is(err, runtime.ErrImageBuild) {
		t.Fatalf("missing ID: err = %v", err)
	}
	if _, err := readBuild(strings.NewReader("{not json"), trace); !errors.Is(err, runtime.ErrImageBuild) {
		t.Fatalf("garbage: err = %v", err)
	}
}

func TestPbFieldsRejectsTruncatedInput(t *testing.T) {
	for _, payload := range [][]byte{{0x0a, 0x05, 'a'}, {0x09, 1, 2}, {0x80}} {
		if _, err := pbFields(payload); err == nil {
			t.Errorf("payload %x accepted", payload)
		}
	}
}
