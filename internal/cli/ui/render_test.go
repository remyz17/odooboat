package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs:\n%s\nwant:\n%s", name, got, want)
	}
}

// sequence covers every record type a renderer handles.
func sequence() []timed {
	at := func(d float64) time.Duration { return time.Duration(d * float64(time.Second)) }
	odoo := app.Subject{Kind: app.ResourceContainer, Name: "ob-shop-dev-odoo-1a2b3c4d", Role: app.RoleOdoo}
	return []timed{
		{0, app.LockWaiting{Holder: &app.LockHolder{Operation: "up", PID: 4242, Host: "laptop", StartedAt: epoch.Add(-time.Minute)}}},
		{0, app.LockWaiting{}},
		{at(1), app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: app.Subject{Kind: app.ResourceImage, Name: "odooboat/environment:abc"}}},
		{at(1.4), app.StepFinished{ID: "1", Outcome: app.OutcomeReused}},
		{at(2), app.StepStarted{ID: "2", Kind: app.StepPull, Subject: app.Subject{Kind: app.ResourceImage, Name: "postgres:16"}}},
		{at(2), app.StepStarted{ID: "3", Parent: "2", Kind: app.StepPull, Subject: app.Subject{Kind: app.ResourceLayer, Name: "a1b2c3d4e5f6"}}},
		{at(2.5), app.Transfer{ID: "3", Current: 10, Total: 100}},
		{at(3), app.StepFinished{ID: "3", Outcome: app.OutcomeDone}},
		{at(3), app.StepStarted{ID: "4", Parent: "2", Kind: app.StepPull, Subject: app.Subject{Kind: app.ResourceLayer, Name: "0f0f0f0f0f0f"}}},
		{at(3), app.StepFinished{ID: "4", Outcome: app.OutcomeKept}},
		{at(4.5), app.StepFinished{ID: "2", Outcome: app.OutcomeDone}},
		{at(5), app.StepStarted{ID: "5", Kind: app.StepBuild, Subject: app.Subject{Kind: app.ResourceImage, Name: "odooboat/environment:def"}}},
		{at(5), app.StepStarted{ID: "6", Parent: "5", Kind: app.StepBuild, Subject: app.Subject{Kind: app.ResourceBuildStep, Name: "[3/5] RUN pip install"}}},
		{at(6), app.StepOutput{ID: "6", Stream: app.StreamStdout, Data: []byte("Collecting nope\nERROR: no matching distribution")}},
		{at(7), app.StepFinished{ID: "6", Outcome: app.OutcomeFailed}},
		{at(7), app.StepFinished{ID: "5", Outcome: app.OutcomeFailed, Err: errors.New("image build failed: exit code: 1")}},
		{at(10), app.StepStarted{ID: "7", Kind: app.StepCreate, Subject: odoo}},
		{at(10), app.StepStarted{ID: "8", Parent: "7", Kind: app.StepWait, Subject: odoo}},
		{at(100), app.StepFinished{ID: "8", Outcome: app.OutcomeDone}},
		{at(100), app.StepFinished{ID: "7", Outcome: app.OutcomeDone}},
		{at(101), app.DatabaseSelected{Name: "shop"}},
		{at(101), app.SessionDegraded{Operation: app.SessionResize, Err: errors.New("no such exec")}},
		{at(101), Tick{}},
		{at(102), Warning{Message: "no masterPassword is configured"}},
		{at(102), ErrorReport{Code: "image-build", Message: "image build failed: exit code: 1", Hint: "rerun with --pull",
			Usage: "Usage:\n  odooboat environment build [flags]\n"}},
	}
}

func render(sink Sink) {
	for _, e := range sequence() {
		sink.Handle(Record{Time: epoch.Add(e.offset), Event: e.event})
	}
}

func TestWriterIgnoresErrorsAfterFailure(t *testing.T) {
	f := &failingWriter{failures: 1}
	w := NewWriter(f)
	if _, err := w.Write([]byte("first")); err != nil {
		t.Fatalf("first write error = %v, want it swallowed", err)
	}
	n, err := w.Write([]byte("second"))
	if n != len("second") || err != nil {
		t.Fatalf("write after failure = %d, %v", n, err)
	}
	if f.calls != 1 {
		t.Fatalf("underlying writer called %d times, want 1", f.calls)
	}
}

type failingWriter struct{ failures, calls int }

func (f *failingWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.failures > 0 {
		f.failures--
		return 0, errors.New("broken pipe")
	}
	return len(p), nil
}

func TestWriterRaw(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.SetRaw(true)
	_, _ = w.Write([]byte("a\nb\n"))
	w.SetRaw(false)
	_, _ = w.Write([]byte("c\n"))
	if b.String() != "a\r\nb\r\nc\n" {
		t.Fatalf("raw output = %q", b.String())
	}
}

func TestPlainGolden(t *testing.T) {
	defer setZone(time.UTC)()
	var b bytes.Buffer
	render(NewPlain(&b))
	golden(t, "plain.golden", b.Bytes())
}

func setZone(loc *time.Location) func() {
	previous := zone
	zone = loc
	return func() { zone = previous }
}

func TestPlainHasNoControlSequences(t *testing.T) {
	var b bytes.Buffer
	render(NewPlain(&b))
	if bytes.ContainsAny(b.Bytes(), "\x1b\r") {
		t.Fatalf("plain output has control bytes: %q", b.String())
	}
}

func TestPlainHeartbeat(t *testing.T) {
	var b bytes.Buffer
	p := NewPlain(&b)
	build := app.Subject{Kind: app.ResourceImage, Name: "img"}
	feed(p,
		timed{0, app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: build}},
		timed{0, app.StepStarted{ID: "2", Parent: "1", Kind: app.StepBuild, Subject: app.Subject{Kind: app.ResourceBuildStep, Name: "RUN x"}}},
	)
	b.Reset()
	expect := func(at time.Duration, want string) {
		t.Helper()
		feed(p, timed{at, Tick{}})
		if b.String() != want {
			t.Fatalf("tick at %v printed %q, want %q", at, b.String(), want)
		}
		b.Reset()
	}
	expect(29*time.Second, "")
	expect(31*time.Second, "build image img: running (31.0s)\n")
	expect(45*time.Second, "")
	expect(62*time.Second, "build image img: running (1m2s)\n")
}

func TestJSONGolden(t *testing.T) {
	var b bytes.Buffer
	render(NewJSON(&b))
	golden(t, "json.golden", b.Bytes())
	known := map[string]bool{"step-started": true, "step-finished": true, "step-output": true, "transfer": true,
		"lock-waiting": true, "database-selected": true, "session-degraded": true, "error": true}
	for _, line := range strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n") {
		var event struct {
			Schema int    `json:"schema"`
			Type   string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Schema != 1 || !known[event.Type] {
			t.Fatalf("line %s: %+v, %v", line, event, err)
		}
	}
}

func TestJSONBase64(t *testing.T) {
	var b bytes.Buffer
	feed(NewJSON(&b), timed{0, app.StepOutput{ID: "1", Stream: app.StreamStdout, Data: []byte{0xff, 0x00}}})
	var event map[string]any
	if err := json.Unmarshal(b.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["encoding"] != "base64" || event["data"] != "/wA=" {
		t.Fatalf("event = %v", event)
	}
}

func TestJSONIgnoresTicksAndWarnings(t *testing.T) {
	var b bytes.Buffer
	feed(NewJSON(&b), timed{0, Tick{}}, timed{0, Warning{Message: "w"}})
	if b.Len() != 0 {
		t.Fatalf("output = %q", b.String())
	}
}

func TestPlainOutputPrefixIsShort(t *testing.T) {
	for name, want := range map[string]string{
		"[builder 4/4] RUN set -eux; venv=/opt/odooboat/venv/bin/python; pip() { uv tool run }": "[builder 4/4]",
		"ob-shop-dev-odoo-1a2b3c4d":                                        "ob-shop-dev-odoo-1a2b3c4d",
		"exporting to image with a name long enough to need cutting short": "exporting to image with a name long e...",
	} {
		var b bytes.Buffer
		feed(NewPlain(&b),
			timed{0, app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: app.Subject{Kind: app.ResourceBuildStep, Name: name}}},
			timed{0, app.StepOutput{ID: "1", Data: []byte("x\n")}},
		)
		if got, wantLine := b.String(), "  "+want+" | x\n"; got != wantLine {
			t.Errorf("%q: output %q, want %q", name, got, wantLine)
		}
	}
}

func TestPlainShortensImageReferences(t *testing.T) {
	hash := strings.Repeat("0123456789abcdef", 4)
	var b bytes.Buffer
	feed(NewPlain(&b),
		timed{0, app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: app.Subject{Kind: app.ResourceImage, Name: "odooboat/environment:" + hash}}},
		timed{0, app.StepFinished{ID: "1", Outcome: app.OutcomeDone}},
	)
	if !strings.Contains(b.String(), "build image odooboat/environment:"+hash[:12]) || strings.Contains(b.String(), hash) {
		t.Fatalf("plain:\n%s", b.String())
	}
}
