package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestWriteFileAtomicallyOverwritesTheWholeFile(t *testing.T) {
	folder := t.TempDir()
	target := filepath.Join(folder, "config.yaml")

	if err := writeFileAtomically(target, []byte("activeenvironment: alpha\n")); err != nil {
		t.Fatalf("writeFileAtomically() error = %v", err)
	}
	if err := writeFileAtomically(target, []byte("activeenvironment: beta\n")); err != nil {
		t.Fatalf("writeFileAtomically() error = %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("unable to read the config: %v", err)
	}
	if got := string(data); got != "activeenvironment: beta\n" {
		t.Errorf("got %q, want the second document in full", got)
	}

	// The temporary file is renamed, so no leftovers are kept next to the config.
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatalf("unable to list the folder: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d files, want only config.yaml", len(entries))
	}
}

func TestWriteFileAtomicallyCreatesMissingFolders(t *testing.T) {
	folder := t.TempDir()
	nested := filepath.Join(folder, ".sailpoint")
	if err := os.Mkdir(nested, 0777); err != nil {
		t.Fatalf("unable to create the folder: %v", err)
	}

	if err := writeFileAtomically(filepath.Join(nested, "config.yaml"), []byte("debug: true\n")); err != nil {
		t.Fatalf("writeFileAtomically() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(nested, "config.yaml")); err != nil {
		t.Fatalf("expected the config to exist: %v", err)
	}
}

func TestSaveConfigMatchesTheViperFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	resetViper(t)

	SetActiveEnvironment("alpha")
	SetBaseUrl("https://alpha.api.identitynow.com")
	SetDebug(true)

	if err := SaveConfig(); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".sailpoint", "config.yaml"))
	if err != nil {
		t.Fatalf("unable to read the saved config: %v", err)
	}
	saved := string(data)

	for _, want := range []string{"activeenvironment: alpha", "baseurl: https://alpha.api.identitynow.com", "debug: true"} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved config is missing %q:\n%s", want, saved)
		}
	}

	// The atomic writer must not change the layout viper produces.
	reference := filepath.Join(home, "reference.yaml")
	other := viper.New()
	other.Set("activeenvironment", "alpha")
	other.Set("environments.alpha.baseurl", "https://alpha.api.identitynow.com")
	other.Set("debug", true)
	if err := other.WriteConfigAs(reference); err != nil {
		t.Fatalf("unable to write the reference config: %v", err)
	}
	expected, err := os.ReadFile(reference)
	if err != nil {
		t.Fatalf("unable to read the reference config: %v", err)
	}
	if strings.TrimSpace(string(expected)) != strings.TrimSpace(saved) {
		t.Errorf("saved config differs from the viper layout:\n got: %s\nwant: %s", saved, string(expected))
	}
}
