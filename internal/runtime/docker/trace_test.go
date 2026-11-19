package docker

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
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

func TestReadBuildRendersProgress(t *testing.T) {
	stream := traceLine(t, vertexPayload(vertexSpec{digest: "sha256:a", name: "[1/2] FROM base", started: true})) +
		traceLine(t, vertexPayload(vertexSpec{digest: "sha256:a", name: "[1/2] FROM base", started: true, cached: true, complete: true})) +
		traceLine(t, append(vertexPayload(vertexSpec{digest: "sha256:b", name: "[2/2] RUN make", started: true}),
			logPayload("sha256:b", "line one\nline two\n")...)) +
		traceLine(t, vertexPayload(vertexSpec{digest: "sha256:b", name: "[2/2] RUN make", started: true, complete: true})) +
		traceLine(t, []byte{0xff, 0xff}) + // undecodable payloads are ignored
		`{"aux":{"ID":"sha256:built"}}` + "\n"
	var progress bytes.Buffer
	id, err := readBuild(strings.NewReader(stream), newTraceRenderer(&progress))
	if err != nil || id != "sha256:built" {
		t.Fatalf("id = %q, %v", id, err)
	}
	want := "#1 [1/2] FROM base\n#1 CACHED\n#2 [2/2] RUN make\n#2 line one\n#2 line two\n#2 DONE\n"
	if progress.String() != want {
		t.Fatalf("progress:\n%s\nwant:\n%s", progress.String(), want)
	}
}

func TestReadBuildReportsFailures(t *testing.T) {
	failing := traceLine(t, vertexPayload(vertexSpec{digest: "sha256:c", name: "RUN false", started: true, complete: true, err: "exit code: 1"})) +
		`{"errorDetail":{"message":"process did not complete successfully: exit code: 1"},"error":"process did not complete successfully: exit code: 1"}` + "\n"
	var progress bytes.Buffer
	_, err := readBuild(strings.NewReader(failing), newTraceRenderer(&progress))
	if !errors.Is(err, runtime.ErrImageBuild) || !strings.Contains(err.Error(), "exit code: 1") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(progress.String(), "#1 ERROR exit code: 1") {
		t.Fatalf("progress = %q", progress.String())
	}
	if _, err := readBuild(strings.NewReader(`{"stream":"Step 1/1"}`+"\n"), newTraceRenderer(&progress)); !errors.Is(err, runtime.ErrImageBuild) {
		t.Fatalf("missing ID: err = %v", err)
	}
	if _, err := readBuild(strings.NewReader("{not json"), newTraceRenderer(&progress)); !errors.Is(err, runtime.ErrImageBuild) {
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
