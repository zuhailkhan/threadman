package cli

import "testing"

func TestVersionPrefersLinkerValue(t *testing.T) {
	old := version
	defer func() { version = old }()

	version = "v9.9.9"
	if got := Version(); got != "v9.9.9" {
		t.Errorf("got %q, want v9.9.9", got)
	}

	version = ""
	if got := Version(); got == "" {
		t.Error("Version must never be empty")
	}
}
