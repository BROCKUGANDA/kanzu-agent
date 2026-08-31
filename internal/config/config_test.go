package config

import (
	"os"
	"path/filepath"
	"testing"
)

// helper: build a Config whose six prompt files live under dir.
func testConfigWithPrompts(t *testing.T, dir string, write func(name, path string)) *Config {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &Config{PromptDir: dir}
	for name, path := range cfg.PromptFiles() {
		write(name, path)
	}
	return cfg
}

func TestCheckPromptFiles_AcceptsRealPrompts(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfigWithPrompts(t, dir, func(name, path string) {
		// A minimal but non-trivial prompt body for every template.
		body := "You are a test prompt for " + name + ".\nThis file has real content.\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	})
	got := cfg.CheckPromptFiles()
	for _, name := range []string{"planning-en", "narration-en", "narration-sw", "narration-lg"} {
		if err := got[name]; err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCheckPromptFiles_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfigWithPrompts(t, dir, func(name, path string) {
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	})
	got := cfg.CheckPromptFiles()
	for name, err := range got {
		if err == nil {
			t.Errorf("%s: expected error for empty file, got nil", name)
		}
	}
}

func TestCheckPromptFiles_RejectsTooSmall(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfigWithPrompts(t, dir, func(name, path string) {
		// 16 bytes — under the 32-byte minimum.
		if err := os.WriteFile(path, []byte("0123456789ABCDEF"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	})
	got := cfg.CheckPromptFiles()
	for name, err := range got {
		if err == nil {
			t.Errorf("%s: expected too-small error, got nil", name)
		}
	}
}

func TestCheckPromptFiles_RejectsBinary(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfigWithPrompts(t, dir, func(name, path string) {
		// 64 bytes of binary data (NULs in the middle).
		data := make([]byte, 64)
		for i := range data {
			data[i] = byte(i % 4)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	})
	got := cfg.CheckPromptFiles()
	for name, err := range got {
		if err == nil {
			t.Errorf("%s: expected binary-rejection error, got nil", name)
		}
	}
}

func TestCheckPromptFiles_RejectsMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{PromptDir: dir}
	got := cfg.CheckPromptFiles()
	if len(got) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(got))
	}
	for name, err := range got {
		if err == nil {
			t.Errorf("%s: expected missing-file error, got nil", name)
		}
	}
}

// F-03f sanity: every CheckPromptFiles result key matches the canonical
// PromptFiles() set. Drift here would silently swallow a missing template.
func TestCheckPromptFiles_KeySetIsStable(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfigWithPrompts(t, dir, func(name, path string) {
		if err := os.WriteFile(path, []byte("real prompt content for "+name), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	})
	got := cfg.CheckPromptFiles()
	want := map[string]string{
		"planning-en": filepath.Join(dir, "prompt-planning-en.txt"),
		"planning-sw": filepath.Join(dir, "prompt-planning-sw.txt"),
		"planning-lg": filepath.Join(dir, "prompt-planning-lg.txt"),
		"narration-en": filepath.Join(dir, "prompt-narration-en.txt"),
		"narration-sw": filepath.Join(dir, "prompt-narration-sw.txt"),
		"narration-lg": filepath.Join(dir, "prompt-narration-lg.txt"),
	}
	if len(got) != len(want) {
		t.Fatalf("CheckPromptFiles returned %d keys, want %d", len(got), len(want))
	}
	for name, err := range got {
		if err != nil {
			t.Errorf("%s: unexpected err = %v", name, err)
		}
	}
}