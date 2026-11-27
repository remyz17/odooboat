package ui

import (
	"strconv"
	"strings"
	"testing"
)

// vt is a minimal terminal for tests: it applies the sequences the tty
// renderer writes and fails the test on any other escape. A newline also
// returns the carriage, as a terminal's output processing does.
type vt struct {
	t        *testing.T
	lines    [][]rune
	row, col int
	written  int
}

func newVT(t *testing.T) *vt { return &vt{t: t} }

func (v *vt) Write(p []byte) (int, error) {
	v.written += len(p)
	rs := []rune(string(p))
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\r':
			v.col = 0
		case '\n':
			v.row++
			v.col = 0
		case '\x1b':
			j := i + 1
			if j >= len(rs) || rs[j] != '[' {
				v.t.Errorf("unexpected escape in %q", p)
				return len(p), nil
			}
			j++
			start := j
			for j < len(rs) && (rs[j] >= '0' && rs[j] <= '9' || rs[j] == ';') {
				j++
			}
			if j >= len(rs) {
				v.t.Errorf("unterminated escape in %q", p)
				return len(p), nil
			}
			v.csi(string(rs[start:j]), rs[j])
			i = j
		default:
			v.put(r)
		}
	}
	return len(p), nil
}

func (v *vt) csi(params string, final rune) {
	n := 1
	if params != "" && final != 'm' {
		n, _ = strconv.Atoi(params)
	}
	switch final {
	case 'm': // colour: ignored
	case 'A':
		v.row = max(0, v.row-n)
	case 'F':
		v.row, v.col = max(0, v.row-n), 0
	case 'J':
		if v.row < len(v.lines) {
			v.lines[v.row] = v.lines[v.row][:min(v.col, len(v.lines[v.row]))]
			v.lines = v.lines[:v.row+1]
		}
	case 'K':
		if v.row < len(v.lines) {
			v.lines[v.row] = v.lines[v.row][:min(v.col, len(v.lines[v.row]))]
		}
	default:
		v.t.Errorf("unexpected sequence ESC[%s%c", params, final)
	}
}

func (v *vt) put(r rune) {
	for len(v.lines) <= v.row {
		v.lines = append(v.lines, nil)
	}
	line := v.lines[v.row]
	for len(line) < v.col {
		line = append(line, ' ')
	}
	if v.col < len(line) {
		line[v.col] = r
	} else {
		line = append(line, r)
	}
	v.lines[v.row] = line
	v.col++
}

// Lines is every line written so far, without trailing empty lines.
func (v *vt) Lines() []string {
	var out []string
	for _, l := range v.lines {
		out = append(out, string(l))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func (v *vt) Text() string {
	if lines := v.Lines(); len(lines) > 0 {
		return strings.Join(lines, "\n") + "\n"
	}
	return ""
}
