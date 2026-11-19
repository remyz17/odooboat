package docker

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// BuildKit reports progress as base64 protobuf control.StatusResponse
// payloads. This reader decodes the few fields the progress log needs, so the
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

type vertexState struct {
	step                  int
	cached, done, started bool
}

// traceRenderer writes a plain progress log: one line when a step starts, is
// cached, completes, or fails, and its output lines prefixed by the step.
type traceRenderer struct {
	w        io.Writer
	vertexes map[string]*vertexState
	steps    int
}

func newTraceRenderer(w io.Writer) *traceRenderer {
	return &traceRenderer{w: w, vertexes: map[string]*vertexState{}}
}

func (r *traceRenderer) vertex(digest string) *vertexState {
	v, ok := r.vertexes[digest]
	if !ok {
		r.steps++
		v = &vertexState{step: r.steps}
		r.vertexes[digest] = v
	}
	return v
}

func (r *traceRenderer) render(payload []byte) {
	fields, err := pbFields(payload)
	if err != nil {
		return
	}
	for _, field := range fields {
		sub, err := pbFields(field.bytes)
		if err != nil {
			continue
		}
		switch field.num {
		case 1:
			r.renderVertex(sub)
		case 3:
			r.renderLog(sub)
		}
	}
}

func (r *traceRenderer) renderVertex(fields []pbField) {
	var digest, name, failure string
	var cached, started, completed bool
	for _, f := range fields {
		switch f.num {
		case 1:
			digest = string(f.bytes)
		case 3:
			name = string(f.bytes)
		case 4:
			cached = f.varint != 0
		case 5:
			started = true
		case 6:
			completed = true
		case 7:
			failure = string(f.bytes)
		}
	}
	if digest == "" {
		return
	}
	v := r.vertex(digest)
	if !v.started && (started || cached) {
		v.started = true
		fmt.Fprintf(r.w, "#%d %s\n", v.step, name)
	}
	switch {
	case v.done:
	case failure != "":
		v.done = true
		fmt.Fprintf(r.w, "#%d ERROR %s\n", v.step, failure)
	case cached && !v.cached:
		v.cached, v.done = true, true
		fmt.Fprintf(r.w, "#%d CACHED\n", v.step)
	case completed:
		v.done = true
		fmt.Fprintf(r.w, "#%d DONE\n", v.step)
	}
}

func (r *traceRenderer) renderLog(fields []pbField) {
	var digest string
	var message []byte
	for _, f := range fields {
		switch f.num {
		case 1:
			digest = string(f.bytes)
		case 4:
			message = f.bytes
		}
	}
	if digest == "" || len(message) == 0 {
		return
	}
	step := r.vertex(digest).step
	for _, line := range strings.Split(strings.TrimRight(string(message), "\n"), "\n") {
		fmt.Fprintf(r.w, "#%d %s\n", step, line)
	}
}

// text writes plain stream output, which builders other than BuildKit emit.
func (r *traceRenderer) text(s string) {
	if s = strings.TrimRight(s, "\n"); s != "" {
		fmt.Fprintln(r.w, s)
	}
}
