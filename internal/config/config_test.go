package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
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
	target := filepath.Join(folder, ".sailpoint", "nested", "config.yaml")

	if err := writeFileAtomically(target, []byte("debug: true\n")); err != nil {
		t.Fatalf("writeFileAtomically() error = %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected the config to exist: %v", err)
	}
	if string(data) != "debug: true\n" {
		t.Errorf("got %q, want %q", string(data), "debug: true\n")
	}
}

func TestSaveConfigRespectsConfigFileUsed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	resetViper(t)

	folder := filepath.Join(home, ".sailpoint")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatalf("unable to create folder: %v", err)
	}
	ymlPath := filepath.Join(folder, "config.yml")
	if err := os.WriteFile(ymlPath, []byte("activeenvironment: initial\n"), 0600); err != nil {
		t.Fatalf("unable to seed config.yml: %v", err)
	}

	if err := InitConfig(); err != nil {
		t.Fatalf("InitConfig() error = %v", err)
	}
	SetActiveEnvironment("updated")

	if err := SaveConfig(); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	data, err := os.ReadFile(ymlPath)
	if err != nil {
		t.Fatalf("unable to read config.yml: %v", err)
	}
	if !strings.Contains(string(data), "activeenvironment: updated") {
		t.Errorf("expected config.yml to be updated, got:\n%s", string(data))
	}

	// Must not create a duplicate config.yaml next to config.yml.
	if _, err := os.Stat(filepath.Join(folder, "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected no duplicate config.yaml, got err=%v", err)
	}
}

func TestWriteFileAtomicallyConcurrentReadersAndWriters(t *testing.T) {
	folder := t.TempDir()
	target := filepath.Join(folder, "config.yaml")

	initial := []byte("activeenvironment: env-0\ndebug: false\n")
	if err := writeFileAtomically(target, initial); err != nil {
		t.Fatalf("initial writeFileAtomically() error = %v", err)
	}

	const writers = 4
	const iterations = 25
	done := make(chan struct{})
	errCh := make(chan error, writers+2)

	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-done:
				return
			default:
				data, err := os.ReadFile(target)
				if err != nil {
					continue
				}
				var parsed map[string]interface{}
				if err := yaml.Unmarshal(data, &parsed); err != nil {
					errCh <- fmt.Errorf("observed invalid yaml: %v (raw: %q)", err, string(data))
					return
				}
				if _, ok := parsed["activeenvironment"]; !ok {
					errCh <- fmt.Errorf("observed truncated yaml missing activeenvironment: %q", string(data))
					return
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	var writerWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(id int) {
			defer writerWG.Done()
			for i := 0; i < iterations; i++ {
				doc := fmt.Sprintf("activeenvironment: env-%d-%d\ndebug: %v\n", id, i, i%2 == 0)
				if err := writeFileAtomically(target, []byte(doc)); err != nil {
					errCh <- fmt.Errorf("writer %d iter %d: %w", id, i, err)
					return
				}
			}
		}(w)
	}

	writerWG.Wait()
	close(done)
	readerWG.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	// Ensure no leftover temp files remain after concurrent writes.
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatalf("unable to list folder: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Errorf("unexpected leftover files: %v", entries)
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
