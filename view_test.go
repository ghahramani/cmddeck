package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenamedAppKeepsExistingConfiguration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	old := filepath.Join(root, "command-catalog", "config.json")
	writeTestFile(t, old, `{"exclude":["old-helper"]}`, 0644)
	if got := defaultConfigPath(); got != old {
		t.Fatalf("legacy config was ignored: %s", got)
	}
	current := filepath.Join(root, "cmddeck", "config.json")
	writeTestFile(t, current, `{}`, 0644)
	if got := defaultConfigPath(); got != current {
		t.Fatalf("new config should take precedence: %s", got)
	}
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
}

func TestCommandPreviewShowsSavedArguments(t *testing.T) {
	b := &browser{arguments: map[string]string{"nvtop": "--no-color"}}
	if got := b.commandLine(Command{Name: "nvtop"}); got != "$ nvtop --no-color" {
		t.Fatal(got)
	}
	if got := formatCount(1234567); got != "1,234,567" {
		t.Fatal(got)
	}
}
