package ui

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"time"
	"unicode/utf8"

	"github.com/remyz17/odooboat/internal/app"
)

// jsonSchema versions the event objects (ADR 0007).
const jsonSchema = 1

// jsonSink writes one object per event (NDJSON), with fixed key order.
type jsonSink struct{ enc *json.Encoder }

func NewJSON(w io.Writer) Sink {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &jsonSink{enc: enc}
}

type jsonHeader struct {
	Schema int    `json:"schema"`
	Type   string `json:"type"`
	Time   string `json:"time"`
}

type jsonSubject struct {
	Kind app.ResourceKind `json:"kind"`
	Name string           `json:"name"`
	Role app.Role         `json:"role,omitempty"`
}

type jsonHolder struct {
	Operation string    `json:"operation"`
	PID       int       `json:"pid"`
	Host      string    `json:"host"`
	StartedAt time.Time `json:"startedAt"`
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *jsonSink) Handle(r Record) {
	header := func(kind string) jsonHeader {
		return jsonHeader{Schema: jsonSchema, Type: kind, Time: r.Time.UTC().Format(time.RFC3339Nano)}
	}
	var v any
	switch e := r.Event.(type) {
	case app.StepStarted:
		v = struct {
			jsonHeader
			ID      string       `json:"id"`
			Parent  string       `json:"parent,omitempty"`
			Kind    app.StepKind `json:"kind"`
			Subject jsonSubject  `json:"subject"`
		}{header("step-started"), e.ID, e.Parent, e.Kind, jsonSubject(e.Subject)}
	case app.StepFinished:
		v = struct {
			jsonHeader
			ID      string      `json:"id"`
			Outcome app.Outcome `json:"outcome"`
			Error   string      `json:"error,omitempty"`
		}{header("step-finished"), e.ID, e.Outcome, errorText(e.Err)}
	case app.StepOutput:
		data, encoding := string(e.Data), ""
		if !utf8.Valid(e.Data) {
			data, encoding = base64.StdEncoding.EncodeToString(e.Data), "base64"
		}
		v = struct {
			jsonHeader
			ID       string     `json:"id"`
			Stream   app.Stream `json:"stream"`
			Data     string     `json:"data"`
			Encoding string     `json:"encoding,omitempty"`
		}{header("step-output"), e.ID, e.Stream, data, encoding}
	case app.Transfer:
		v = struct {
			jsonHeader
			ID      string `json:"id"`
			Current int64  `json:"current"`
			Total   int64  `json:"total"`
		}{header("transfer"), e.ID, e.Current, e.Total}
	case app.LockWaiting:
		var holder *jsonHolder
		if h := e.Holder; h != nil {
			holder = &jsonHolder{Operation: h.Operation, PID: h.PID, Host: h.Host, StartedAt: h.StartedAt.UTC()}
		}
		v = struct {
			jsonHeader
			Holder *jsonHolder `json:"holder"`
		}{header("lock-waiting"), holder}
	case app.DatabaseSelected:
		v = struct {
			jsonHeader
			Name string `json:"name"`
		}{header("database-selected"), e.Name}
	case app.SessionDegraded:
		v = struct {
			jsonHeader
			Operation app.SessionOperation `json:"operation"`
			Error     string               `json:"error"`
		}{header("session-degraded"), e.Operation, errorText(e.Err)}
	case ErrorReport:
		v = struct {
			jsonHeader
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint,omitempty"`
		}{header("error"), e.Code, e.Message, e.Hint}
	default:
		return // ticks and warnings have no JSON form
	}
	_ = s.enc.Encode(v)
}
