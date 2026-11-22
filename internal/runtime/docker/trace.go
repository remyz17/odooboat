package docker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/remyz17/odooboat/internal/runtime"
)

// BuildKit reports progress as base64 protobuf control.StatusResponse
// payloads. This reader decodes the few fields progress reports need, so the
// adapter does not depend on BuildKit or protobuf libraries:
//
//	StatusResponse { repeated Vertex vertexes = 1; repeated VertexLog logs = 3; }
//	Vertex         { string digest = 1; string name = 3; bool cached = 4;
//	                 Timestamp started = 5; Timestamp completed = 6; string error = 7; }
//	VertexLog      { string vertex = 1; int64 stream = 3; bytes msg = 4; }
//
// Unknown fields are skipped; a payload that cannot be decoded is ignored, so
// progress is best effort and never fails a build.

type pbField struct {
	num    int
	varint uint64
	bytes  []byte
}

const (
	wireVarint = 0
	wireFixed  = 1
	wireBytes  = 2
	wireFixed4 = 5
)

func pbFields(b []byte) ([]pbField, error) {
	var out []pbField
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errors.New("invalid field key")
		}
		b = b[n:]
		field := pbField{num: int(key >> 3)}
		switch key & 7 {
		case wireVarint:
			value, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errors.New("invalid varint")
			}
			field.varint, b = value, b[n:]
		case wireBytes:
			length, n := binary.Uvarint(b)
			if n <= 0 || length > uint64(len(b)-n) {
				return nil, errors.New("invalid length")
			}
			field.bytes, b = b[n:n+int(length)], b[n+int(length):]
		case wireFixed:
			if len(b) < 8 {
				return nil, errors.New("truncated fixed64")
			}
			b = b[8:]
		case wireFixed4:
			if len(b) < 4 {
				return nil, errors.New("truncated fixed32")
			}
			b = b[4:]
		default:
			return nil, fmt.Errorf("unsupported wire type %d", key&7)
		}
		out = append(out, field)
	}
	return out, nil
}

// traceDecoder turns BuildKit status payloads into vertex progress.
type traceDecoder struct {
	emit   runtime.ProgressFunc
	names  map[string]string
	states map[string]runtime.ProgressState
	order  []string
}

func newTraceDecoder(emit runtime.ProgressFunc) *traceDecoder {
	return &traceDecoder{emit: emit, names: map[string]string{}, states: map[string]runtime.ProgressState{}}
}

type vertexUpdate struct {
	digest, name, failure      string
	cached, started, completed bool
}

// payload decodes one StatusResponse. Within it, starts are reported first,
// then logs, then terminal states: BuildKit sends a vertex's last logs in the
// same status as its completion.
func (d *traceDecoder) payload(payload []byte) {
	fields, err := pbFields(payload)
	if err != nil {
		return
	}
	var vertexes []vertexUpdate
	type vertexLog struct {
		digest string
		msg    []byte
	}
	var logs []vertexLog
	for _, field := range fields {
		sub, err := pbFields(field.bytes)
		if err != nil {
			continue
		}
		switch field.num {
		case 1:
			if v := decodeVertex(sub); v.digest != "" {
				vertexes = append(vertexes, v)
			}
		case 3:
			var l vertexLog
			for _, f := range sub {
				switch f.num {
				case 1:
					l.digest = string(f.bytes)
				case 4:
					l.msg = f.bytes
				}
			}
			if l.digest != "" && len(l.msg) > 0 {
				logs = append(logs, l)
			}
		}
	}
	for _, v := range vertexes {
		if v.name != "" {
			d.names[v.digest] = v.name
		}
		if v.started || v.cached || v.completed || v.failure != "" {
			d.start(v.digest)
		}
	}
	for _, l := range logs {
		if d.states[l.digest].Terminal() {
			// Output of a finished vertex belongs to the build as a whole.
			d.emit.Report(runtime.Progress{Output: bytes.Clone(l.msg)})
			continue
		}
		d.start(l.digest)
		d.report(l.digest, runtime.ProgressRunning, bytes.Clone(l.msg))
	}
	for _, v := range vertexes {
		if d.states[v.digest].Terminal() {
			continue
		}
		switch {
		case v.failure != "":
			d.report(v.digest, runtime.ProgressFailed, nil)
		case v.cached:
			d.report(v.digest, runtime.ProgressCached, nil)
		case v.completed:
			d.report(v.digest, runtime.ProgressDone, nil)
		}
	}
}

func decodeVertex(fields []pbField) vertexUpdate {
	var v vertexUpdate
	for _, f := range fields {
		switch f.num {
		case 1:
			v.digest = string(f.bytes)
		case 3:
			v.name = string(f.bytes)
		case 4:
			v.cached = f.varint != 0
		case 5:
			v.started = true
		case 6:
			v.completed = true
		case 7:
			v.failure = string(f.bytes)
		}
	}
	return v
}

func (d *traceDecoder) start(digest string) {
	if _, ok := d.states[digest]; ok {
		return
	}
	d.order = append(d.order, digest)
	d.report(digest, runtime.ProgressStarted, nil)
}

func (d *traceDecoder) report(digest string, state runtime.ProgressState, output []byte) {
	d.states[digest] = state
	d.emit.Report(runtime.Progress{
		ID: digest, Kind: runtime.ProgressVertex, Name: d.names[digest], State: state, Output: output,
	})
}

// text reports plain stream output, which builders other than BuildKit emit.
func (d *traceDecoder) text(s string) {
	if s != "" {
		d.emit.Report(runtime.Progress{Output: []byte(s)})
	}
}

// close ends every vertex still open, as failed when the build did not finish.
func (d *traceDecoder) close(failed bool) {
	state := runtime.ProgressDone
	if failed {
		state = runtime.ProgressFailed
	}
	for _, digest := range d.order {
		if !d.states[digest].Terminal() {
			d.report(digest, state, nil)
		}
	}
	d.order = nil
}
