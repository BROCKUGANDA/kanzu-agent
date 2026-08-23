// Package config resolves Kanzu Agent's runtime configuration.
//
// Single source of truth: metadata.json. The ADTC profiler reads
// `_runtime.model_path` from that file to locate the GGUF; Kanzu reads the same
// key, so the app and the evaluator can never disagree about which weights are
// in play. Underscore-prefixed top-level keys are stripped by the profiler
// before schema validation, so they are safe to use for app configuration.
//
// No value here is fetched from the network. Every path is local.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Metadata mirrors the subset of metadata.json that Kanzu needs.
type Metadata struct {
	TeamID        string   `json:"team_id"`
	Domain        string   `json:"domain"`
	LanguageScope []string `json:"language_scope"`
	Model         struct {
		Name           string `json:"name"`
		Runtime        string `json:"runtime"`
		Quantization   string `json:"quantization"`
		ParamsEstimate string `json:"parameters_estimate"`
	} `json:"model"`
	Runtime struct {
		ModelPath  string `json:"model_path"`
		ModelSHA   string `json:"model_sha256"`
		ModelBytes int64  `json:"model_bytes"`
		BaseModel  string `json:"base_model"`
		Arch       string `json:"architecture"`
		FineTuned  bool   `json:"fine_tuned"`
	} `json:"_runtime"`
	Kanzu struct {
		AppVersion      string   `json:"app_version"`
		InferenceBinary string   `json:"inference_binary"`
		InferenceFlags  []string `json:"inference_flags"`
		ContextTokens   int      `json:"context_tokens"`
		MaxThreads      int      `json:"max_threads"`
		ThermalCeilingC float64  `json:"thermal_ceiling_c"`
		ThermalResumeC  float64  `json:"thermal_resume_c"`
		DutyCycle       float64  `json:"duty_cycle"`
	} `json:"_kanzu"`
}

// Config is the fully resolved, absolute-path configuration.
type Config struct {
	RepoRoot      string
	MetadataPath  string
	ModelPath     string
	DBPath        string
	PromptDir     string // var/prompts/ — versioned system prompts for narration and planning
	KnowledgeDir  string
	FixturesDir   string
	LlamaCLI      string
	Lang          string
	Threads       int
	ContextTokens int
	MaxTokens     int
	Seed          int
	Temperature   float64
	TopP          float64
	Planner       string

	// PromptBudgetChars caps how much evidence is packed into a prompt.
	//
	// This is a latency control, not a correctness one. Measured on the dev CPU,
	// prompt processing runs at ~57 tok/s against ~22 tok/s generation, so a
	// prompt token costs roughly 40% of what an output token costs and there are
	// far more of them. A 512-token prompt already costs 9 seconds before the
	// first output token appears. Capping the evidence block keeps an interactive
	// request inside roughly half a minute on the target hardware; the
	// deterministic findings are complete regardless, since they are rendered
	// without the model.
	PromptBudgetChars int

	ThermalCeilingC float64
	ThermalResumeC  float64
	DutyCycle       float64

	Meta Metadata
}

// ErrNoRepoRoot is returned when metadata.json cannot be located.
var ErrNoRepoRoot = errors.New("could not locate metadata.json: run kanzu from inside the submission repo or set KANZU_ROOT")

// findRepoRoot walks upward from each candidate start directory looking for
// metadata.json. Walking up (rather than assuming cwd) lets `kanzu` run from
// any subdirectory, which matters for the demo recording.
func findRepoRoot() (string, error) {
	if v := strings.TrimSpace(os.Getenv("KANZU_ROOT")); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", err
		}
		if fileExists(filepath.Join(abs, "metadata.json")) {
			return abs, nil
		}
		return "", fmt.Errorf("%w (KANZU_ROOT=%s has no metadata.json)", ErrNoRepoRoot, abs)
	}

	starts := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}

	for _, start := range starts {
		dir := start
		for {
			if fileExists(filepath.Join(dir, "metadata.json")) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", ErrNoRepoRoot
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Load resolves configuration from metadata.json plus environment overrides.
//
// It deliberately does not fail when the GGUF is absent: subcommands that do
// not need the model (init, scan, doctor) must still work so an operator can
// diagnose a half-provisioned install. Callers that need weights call
// RequireModel.
func Load() (*Config, error) {
	root, err := findRepoRoot()
	if err != nil {
		return nil, err
	}

	metaPath := filepath.Join(root, "metadata.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", metaPath, err)
	}
	var meta Metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", metaPath, err)
	}
	if meta.Runtime.ModelPath == "" {
		return nil, fmt.Errorf("%s: _runtime.model_path is empty", metaPath)
	}

	cfg := &Config{
		RepoRoot:          root,
		MetadataPath:      metaPath,
		ModelPath:         filepath.Join(root, filepath.FromSlash(meta.Runtime.ModelPath)),
		DBPath:            filepath.Join(root, "var", "kanzu.db"),
		PromptDir:         filepath.Join(root, "var", "prompts"),
		KnowledgeDir:      filepath.Join(root, "knowledge"),
		FixturesDir:       filepath.Join(root, "fixtures"),
		LlamaCLI:          firstNonEmpty(meta.Kanzu.InferenceBinary, "llama-cli"),
		Lang:              "en",
		ContextTokens:     orInt(meta.Kanzu.ContextTokens, 2048),
		// 360 truncated a multi-finding SAR note mid-sentence. With
		// _kanzu.context_tokens at 4096 there is headroom for a complete
		// note; override per run with KANZU_MAX_TOKENS.
		MaxTokens:         700,
		PromptBudgetChars: 3000,
		Seed:              42,
		Temperature:       0.25,
		TopP:              0.9,
		Planner:           "hybrid",
		ThermalCeilingC:   orFloat(meta.Kanzu.ThermalCeilingC, 82.0),
		ThermalResumeC:    orFloat(meta.Kanzu.ThermalResumeC, 72.0),
		DutyCycle:         orFloat(meta.Kanzu.DutyCycle, 0.7),
		Meta:              meta,
	}

	cfg.Threads = defaultThreads(meta.Kanzu.MaxThreads)

	// Environment overrides — all local, none of them network-derived.
	if v := os.Getenv("KANZU_MODEL"); v != "" {
		cfg.ModelPath = absOr(root, v)
	}
	if v := os.Getenv("KANZU_DB"); v != "" {
		cfg.DBPath = absOr(root, v)
	}
	if v := os.Getenv("KANZU_LLAMA_CLI"); v != "" {
		cfg.LlamaCLI = v
	}
	if v := os.Getenv("KANZU_LANG"); v != "" {
		cfg.Lang = normaliseLang(v)
	}
	if n, ok := envInt("KANZU_THREADS"); ok && n > 0 {
		cfg.Threads = n
	}
	if n, ok := envInt("KANZU_MAX_TOKENS"); ok && n > 0 {
		cfg.MaxTokens = n
	}
	if n, ok := envInt("KANZU_PROMPT_BUDGET"); ok && n >= 600 {
		cfg.PromptBudgetChars = n
	}
	if n, ok := envInt("KANZU_SEED"); ok && n >= 0 {
		cfg.Seed = n
	}
	if f, ok := envFloat("KANZU_DUTY_CYCLE"); ok && f > 0 && f <= 1 {
		cfg.DutyCycle = f
	}
	if v := os.Getenv("KANZU_PLANNER"); v != "" {
		cfg.Planner = strings.ToLower(strings.TrimSpace(v))
	}

	return cfg, nil
}

// RequireModel verifies the GGUF is present and plausibly the expected artifact.
// It checks size (cheap) but not sha256 (940 MiB hash on every launch would add
// seconds to an interactive tool); download_model.sh owns full verification.
func (c *Config) RequireModel() error {
	st, err := os.Stat(c.ModelPath)
	if err != nil {
		return fmt.Errorf("model weights not found at %s — run: bash download_model.sh", c.ModelPath)
	}
	if want := c.Meta.Runtime.ModelBytes; want > 0 && st.Size() != want {
		return fmt.Errorf("model at %s is %d bytes, metadata declares %d — re-run download_model.sh",
			c.ModelPath, st.Size(), want)
	}
	return nil
}

// EnsureStateDir creates the directory holding the SQLite ledger.
func (c *Config) EnsureStateDir() error {
	return os.MkdirAll(filepath.Dir(c.DBPath), 0o755)
}

// PromptFiles returns the six required named prompt file paths (EN, SW, LG).
func (c *Config) PromptFiles() map[string]string {
	d := c.PromptDir
	return map[string]string{
		"narration-en": filepath.Join(d, "prompt-narration-en.txt"),
		"narration-sw": filepath.Join(d, "prompt-narration-sw.txt"),
		"narration-lg": filepath.Join(d, "prompt-narration-lg.txt"),
		"planning-en":  filepath.Join(d, "prompt-planning-en.txt"),
		"planning-sw":  filepath.Join(d, "prompt-planning-sw.txt"),
		"planning-lg":  filepath.Join(d, "prompt-planning-lg.txt"),
	}
}

// CheckPromptFiles verifies all six prompt templates exist and are non-empty.
// Returns a map of name → error (nil when the file is good).
func (c *Config) CheckPromptFiles() map[string]error {
	results := make(map[string]error, 6)
	for name, path := range c.PromptFiles() {
		st, err := os.Stat(path)
		if err != nil {
			results[name] = fmt.Errorf("missing: %s", path)
			continue
		}
		if st.Size() == 0 {
			results[name] = fmt.Errorf("empty: %s", path)
			continue
		}
		results[name] = nil
	}
	return results
}

// defaultThreads picks a thread count that leaves the machine responsive and
// avoids sustained all-core saturation, which is what drives the ADTC thermal
// penalty. On the 4-core Standard Laptop this yields 3.
func defaultThreads(capHint int) int {
	n := runtime.NumCPU() - 1
	if n < 1 {
		n = 1
	}
	hint := capHint
	if hint <= 0 {
		hint = 4
	}
	if n > hint {
		n = hint
	}
	return n
}

func normaliseLang(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasPrefix(v, "sw"):
		return "sw"
	case strings.HasPrefix(v, "lg"), v == "luganda":
		return "lg"
	default:
		return "en"
	}
}

func absOr(root, v string) string {
	if filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(root, filepath.FromSlash(v))
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func orFloat(v, def float64) float64 {
	if v <= 0 {
		return def
	}
	return v
}

func envInt(key string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func envFloat(key string) (float64, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
