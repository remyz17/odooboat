package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestHint(t *testing.T) {
	hinted := WithHint(fmt.Errorf("%w: x", ErrNotRunning), "run odooboat up")
	if got := Hint(hinted); got != "run odooboat up" {
		t.Fatalf("Hint = %q", got)
	}
	if !errors.Is(hinted, ErrNotRunning) {
		t.Fatal("errors.Is lost the sentinel")
	}
	if strings.Contains(hinted.Error(), "odooboat up") {
		t.Fatalf("Error() = %q contains the hint", hinted.Error())
	}
	if got := Hint(fmt.Errorf("wrap: %w", hinted)); got != "run odooboat up" {
		t.Fatalf("Hint through a wrap = %q", got)
	}
	if got := Hint(errors.New("plain")); got != "" {
		t.Fatalf("Hint without one = %q", got)
	}
	if got := Hint(WithHint(hinted, "outer")); got != "outer" {
		t.Fatalf("outermost hint = %q", got)
	}
}
