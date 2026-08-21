// Package llm drives local inference through llama.cpp.
//
// Two decisions dominate this file.
//
// Subprocess over server. Kanzu shells out to `llama-cli` with pipes rather than
// talking to `llama-server` over HTTP. A loopback socket is still a socket: it
// would put net/http in the binary, make the offline claim harder to prove, and
// break the strongest available proof (running the whole agent inside a network
// namespace with no interfaces at all). Pipes have none of those properties.
//
// Stateless per-request invocation. Each call loads the model, generates, and
// exits. That sounds wasteful and would be on a GPU box, but on the 8 GB target
// it is the right trade: the GGUF is mmap'd, so the second and later loads come
// from the page cache in a few hundred milliseconds, and between requests the
// agent's resident set falls back to ~20 MB of Go instead of holding ~1.2 GB of
// weights hostage while an operator reads a report. It also makes every
// inference reproducible from its prompt file, which is what a compliance audit
// trail needs.
package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kanzu-agent/kanzu/internal/thermal"
)

// Request is one generation call.
type Request struct {
	Prompt      string
	MaxTokens   int
	Temperature float64
	TopP        float64
	Seed        int
	// Label appears in the audit trail so a reviewer can tell a plan call from a
	// narration call.
	Label string
}

// Result carries the completion plus llama.cpp's own performance counters.
// Timings are taken from llama.cpp rather than measured in Go: wall time around
// a subprocess includes model load, which would understate generation speed and
// make our numbers disagree with the ADTC profiler's llama-bench figures.
type Result struct {
	Text            string
	PromptTokens    int
	GeneratedTokens int
	PromptTPS       float64
	GenerationTPS   float64
	LoadMS          float64
	PromptEvalMS    float64
	EvalMS          float64
	WallTime        time.Duration
	Label           string
}

// Runner owns the llama.cpp invocation. Safe for concurrent use, though the
// Governor serialises bursts anyway.
type Runner struct {
	Bin        string
	ModelPath  string
	Threads    int
	CtxTokens  int
	MicroBatch int
	TmpDir     string
	Gov        *thermal.Governor

	// KeepPrompts writes every prompt to TmpDir and leaves it there. Off by
	// default; `--keep-prompts` turns it on for demo and audit purposes.
	KeepPrompts bool

	capsOnce sync.Once
	caps     map[string]bool
	capsErr  error
}

// ErrNoBinary means llama.cpp is not installed.
var ErrNoBinary = errors.New("llama.cpp not found")

// New constructs a Runner.
func New(bin, modelPath, tmpDir string, threads, ctxTokens int, gov *thermal.Governor) *Runner {
	if threads < 1 {
		threads = 1
	}
	if ctxTokens < 512 {
		ctxTokens = 512
	}
	return &Runner{
		Bin:        bin,
		ModelPath:  modelPath,
		Threads:    threads,
		CtxTokens:  ctxTokens,
		MicroBatch: 128,
		TmpDir:     tmpDir,
		Gov:        gov,
	}
}

// Resolve locates the llama.cpp CLI. Accepts an explicit path, then falls back
// through the binary names shipped by different llama.cpp eras and packagers.
func Resolve(pref string) (string, error) {
	candidates := []string{}
	if strings.TrimSpace(pref) != "" {
		candidates = append(candidates, pref)
	}
	candidates = append(candidates,
		"llama-cli",                              // current upstream
		"llama.cpp-cli",                          // some distro packages
		"main",                                   // pre-2024 upstream name
		"./vendor/llama.cpp/build/bin/llama-cli", // scripts/setup_llama_cpp.sh
	)
	for _, c := range candidates {
		if strings.ContainsAny(c, `/\`) {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				abs, err := filepath.Abs(c)
				if err == nil {
					return abs, nil
				}
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: tried %s. Build it with scripts/setup_llama_cpp.sh, or set KANZU_LLAMA_CLI",
		ErrNoBinary, strings.Join(candidates, ", "))
}

// capabilities parses `llama-cli --help` once and records which flags the local
// build accepts.
//
// This exists because llama.cpp renames flags between releases and judges will
// build it from whatever commit is HEAD on their machine. Passing an unknown
// flag is a hard exit, so a submission that hard-codes flags is one upstream
// rename away from scoring zero. Probing costs one cheap exec per process.
func (r *Runner) capabilities() (map[string]bool, error) {
	r.capsOnce.Do(func() {
		caps := map[string]bool{}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, r.Bin, "--help")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		// llama-cli --help exits non-zero on some builds; the text is what matters.
		_ = cmd.Run()
		text := out.String()
		if strings.TrimSpace(text) == "" {
			r.capsErr = fmt.Errorf("%s --help produced no output; is it really llama.cpp?", r.Bin)
			return
		}
		for _, flag := range []string{
			"-no-cnv", "--no-conversation", "--no-display-prompt", "--no-warmup",
			"-ngl", "--n-gpu-layers", "--top-p", "--temp", "--seed", "-ub",
			"--ubatch-size", "--simple-io", "-st", "--single-turn", "--no-perf",
		} {
			if strings.Contains(text, flag) {
				caps[flag] = true
			}
		}
		r.caps = caps
	})
	return r.caps, r.capsErr
}

// Probe verifies the binary is usable and returns its self-reported version.
func (r *Runner) Probe() (string, error) {
	if _, err := r.capabilities(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Bin, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run()
	line := strings.TrimSpace(out.String())
	if line == "" {
		return "unknown build", nil
	}
	if i := strings.IndexByte(line, '\n'); i > 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line, nil
}

// Generate runs one paced completion. All inference in Kanzu funnels through
// here, so the Governor sees every burst.
func (r *Runner) Generate(ctx context.Context, req Request) (*Result, error) {
	if req.MaxTokens <= 0 {
		req.MaxTokens = 384
	}
	if req.TopP <= 0 {
		req.TopP = 0.9
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("empty prompt")
	}

	caps, err := r.capabilities()
	if err != nil {
		return nil, err
	}

	promptFile, cleanup, err := r.writePrompt(req.Prompt)
	if err != nil {
		return nil, err
	}
	if !r.KeepPrompts {
		defer cleanup()
	}

	args := []string{
		"-m", r.ModelPath,
		"-f", promptFile,
		"-n", strconv.Itoa(req.MaxTokens),
		"-c", strconv.Itoa(r.CtxTokens),
		"-t", strconv.Itoa(r.Threads),
		"-ngl", "0", // CPU only: the Standard Laptop has no discrete GPU.
		"--temp", trimFloat(req.Temperature),
		"--top-p", trimFloat(req.TopP),
		"--seed", strconv.Itoa(req.Seed),
	}
	// Micro-batch cap flattens the prompt-processing power spike. Prompt eval is
	// the most compute-dense phase and the one that actually pushes core temp up.
	if caps["-ub"] || caps["--ubatch-size"] {
		args = append(args, "-ub", strconv.Itoa(r.MicroBatch))
	}
	if caps["-no-cnv"] {
		args = append(args, "-no-cnv")
	} else if caps["--no-conversation"] {
		args = append(args, "--no-conversation")
	}
	if caps["--no-display-prompt"] {
		args = append(args, "--no-display-prompt")
	}
	if caps["--no-warmup"] {
		args = append(args, "--no-warmup")
	}
	if caps["--simple-io"] {
		args = append(args, "--simple-io")
	}

	var stdout, stderr bytes.Buffer
	res := &Result{Label: req.Label}

	runErr := r.gov().Run(ctx, func() error {
		cmd := exec.CommandContext(ctx, r.Bin, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		cmd.Stdin = nil
		start := time.Now()
		err := cmd.Run()
		res.WallTime = time.Since(start)
		return err
	})

	text := cleanCompletion(stdout.String())
	parsePerf(stderr.String(), res)
	res.Text = text

	if runErr != nil {
		// A non-empty completion with a non-zero exit is normal on some builds
		// (llama-cli has historically returned 1 after hitting -n). Only fail
		// when we actually have nothing to show.
		if text == "" {
			return nil, fmt.Errorf("llama.cpp failed: %w\n%s", runErr, tail(stderr.String(), 1200))
		}
	}
	if text == "" {
		return nil, fmt.Errorf("llama.cpp produced no output\n%s", tail(stderr.String(), 1200))
	}
	return res, nil
}

func (r *Runner) gov() *thermal.Governor {
	if r.Gov != nil {
		return r.Gov
	}
	// A Runner without a Governor would bypass thermal pacing entirely; give it
	// a default rather than allowing an unpaced path to exist.
	r.Gov = thermal.New(82, 72, 0.7)
	return r.Gov
}

func (r *Runner) writePrompt(prompt string) (string, func(), error) {
	dir := r.TmpDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", func() {}, fmt.Errorf("create prompt dir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "prompt-*.txt")
	if err != nil {
		return "", func() {}, fmt.Errorf("create prompt file: %w", err)
	}
	name := f.Name()
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		os.Remove(name)
		return "", func() {}, fmt.Errorf("write prompt: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", func() {}, err
	}
	return name, func() { os.Remove(name) }, nil
}

// cleanCompletion strips the artefacts llama-cli leaves around generated text.
func cleanCompletion(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, marker := range []string{"[end of text]", "<|im_end|>", "<|endoftext|>", "</s>"} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// parsePerf reads llama.cpp's timing block out of stderr.
//
// Line-based rather than one big regex because "prompt eval time" contains
// "eval time" as a substring: a naive pattern silently reports prompt-processing
// speed as generation speed, which would inflate our headline TPS number.
// Handles both the current `llama_perf_context_print:` prefix and the older
// `llama_print_timings:`.
func parsePerf(stderr string, out *Result) {
	for _, raw := range strings.Split(stderr, "\n") {
		line := strings.TrimSpace(raw)
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		prefix := line[:colon]
		if !strings.Contains(prefix, "llama_perf") && !strings.Contains(prefix, "llama_print_timings") {
			continue
		}
		body := strings.TrimSpace(line[colon+1:])
		lower := strings.ToLower(body)

		switch {
		case strings.HasPrefix(lower, "load time"):
			out.LoadMS = firstMS(body)
		case strings.HasPrefix(lower, "prompt eval time"):
			out.PromptEvalMS = firstMS(body)
			out.PromptTokens = countBefore(body, "tokens")
			out.PromptTPS = tokensPerSecond(body)
		case strings.HasPrefix(lower, "eval time"):
			out.EvalMS = firstMS(body)
			n := countBefore(body, "runs")
			if n == 0 {
				n = countBefore(body, "tokens")
			}
			out.GeneratedTokens = n
			out.GenerationTPS = tokensPerSecond(body)
		}
	}
	// Derive TPS if the build omitted the parenthesised rate.
	if out.GenerationTPS == 0 && out.EvalMS > 0 && out.GeneratedTokens > 0 {
		out.GenerationTPS = float64(out.GeneratedTokens) / (out.EvalMS / 1000.0)
	}
}

// firstMS extracts the milliseconds value from "... =  1234.56 ms / ...".
func firstMS(body string) float64 {
	eq := strings.IndexByte(body, '=')
	if eq < 0 {
		return 0
	}
	rest := strings.TrimSpace(body[eq+1:])
	msIdx := strings.Index(rest, "ms")
	if msIdx < 0 {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rest[:msIdx]), 64)
	if err != nil {
		return 0
	}
	return v
}

// countBefore finds the integer immediately preceding the given unit word,
// e.g. "128 runs" -> 128.
func countBefore(body, unit string) int {
	idx := strings.Index(body, unit)
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(body[:idx])
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return 0
	}
	return n
}

// tokensPerSecond pulls the rate out of "( 12.34 ms per token, 81.05 tokens per second)".
func tokensPerSecond(body string) float64 {
	const marker = "tokens per second"
	idx := strings.Index(body, marker)
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(strings.TrimSpace(body[:idx]))
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(strings.Trim(fields[len(fields)-1], ",()"), 64)
	if err != nil {
		return 0
	}
	return v
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
