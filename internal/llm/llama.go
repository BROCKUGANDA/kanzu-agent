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
	"runtime"
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
	// RepeatPenalty and RepeatLastN suppress the degenerate repetition loops the
	// 1.5B model falls into on long evidence blocks. Zero means "use the
	// defaults applied in Generate".
	RepeatPenalty float64
	RepeatLastN   int
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
//
// A Runner without a Governor would bypass thermal pacing entirely; when the
// caller passes nil this installs a default rather than allowing an unpaced
// path to exist. Eager initialisation here is what makes the "safe for
// concurrent use" contract true: the lazy fallback it replaces wrote r.Gov
// from whichever goroutine got there first.
func New(bin, modelPath, tmpDir string, threads, ctxTokens int, gov *thermal.Governor) *Runner {
	if threads < 1 {
		threads = 1
	}
	if ctxTokens < 512 {
		ctxTokens = 512
	}
	if gov == nil {
		gov = thermal.New(82, 72, 0.7)
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
		// Try llama-completion first when on Windows: the official prebuilt
		// win-cpu-x64 releases ship llama-completion.exe which reliably loads,
		// whereas llama-cli.exe in those same releases can fail to start due to
		// how its multi-binary wrapper resolves DLLs.
		"./vendor/llama.cpp/build/bin/llama-completion",
		"./vendor/llama.cpp/build/bin/llama-completion.exe",
		"llama-completion",
		"llama-completion.exe",
		"llama-cli",
		"llama-cli.exe",
		"llama.cpp-cli",
		"llama.cpp-cli.exe",
		"./vendor/llama.cpp/build/bin/llama-cli",
		"./vendor/llama.cpp/build/bin/llama-cli.exe",
	)
	// The pre-2024 upstream binary was named `main`, but on Windows a bare
	// `main` resolves through PATHEXT to system applets such as main.cpl,
	// which is not llama.cpp. Accept it only on non-Windows systems, and on
	// Windows only with an explicit executable extension.
	if runtime.GOOS != "windows" {
		candidates = append(candidates, "main")
	} else {
		candidates = append(candidates, "main.exe", "main.bat")
	}
	for _, c := range candidates {
		if strings.ContainsAny(c, `/\`) {
			abs := c
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				if a, err := filepath.Abs(c); err == nil {
					abs = a
				}
				if probeRuns(abs) {
					return abs, nil
				}
			}
			// Windows: the same relative candidate may also exist as name.exe.
			if runtime.GOOS == "windows" && filepath.Ext(c) == "" {
				withExt := c + ".exe"
				if st, err := os.Stat(withExt); err == nil && !st.IsDir() {
					if a, err := filepath.Abs(withExt); err == nil {
						withExt = a
					}
					if probeRuns(withExt) {
						return withExt, nil
					}
				}
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil && probeRuns(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: tried %s. Install llama.cpp (winget install ggml.llamacpp on Windows, or scripts/setup_llama_cpp.sh), or set KANZU_LLAMA_CLI",
		ErrNoBinary, strings.Join(candidates, ", "))
}

// probeRuns executes `bin --help` and returns true if the process starts and
// produces any output (exit code is ignored — many llama.cpp builds exit
// non-zero on --help). Returns false if the process cannot be launched at all,
// which happens when required DLLs are missing on Windows.
func probeRuns(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--help")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run()
	return strings.TrimSpace(out.String()) != ""
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
			"--repeat-penalty", "--repeat-last-n",
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
	if req.RepeatPenalty <= 0 {
		req.RepeatPenalty = 1.15
	}
	if req.RepeatLastN <= 0 {
		// Wider than a single finding so the penalty spans a restated block
		// rather than only adjacent tokens.
		req.RepeatLastN = 256
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
	// F-04: register cleanup before any further work, so even a panic inside
	// capabilities() or a write-failure below still removes the prompt file.
	// The cleanup is idempotent (os.Remove returns an error we ignore when the
	// file is already gone).
	defer cleanup()

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
	// llama.cpp defaults --repeat-penalty to 1.0, i.e. disabled. Left off, the
	// 1.5B model degenerates into a repetition loop when narrating a long
	// evidence block — most visibly in Kiswahili and Luganda, where it restated
	// the same finding for consecutive transaction ids until it hit the token
	// cap. A mild penalty over a window wider than one finding stops that
	// without distorting the figures, which are copied from EVIDENCE.
	if caps["--repeat-penalty"] {
		args = append(args, "--repeat-penalty", trimFloat(req.RepeatPenalty))
		if caps["--repeat-last-n"] {
			args = append(args, "--repeat-last-n", strconv.Itoa(req.RepeatLastN))
		}
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

	runErr := r.Gov.Run(ctx, func() error {
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

// writePrompt materialises a prompt string to a unique temp file in TmpDir
// (or os.TempDir() when TmpDir is empty). Returns the path and a cleanup
// function that removes it; the cleanup is a no-op when KeepPrompts is set
// (so demo and audit workflows can inspect the on-disk prompt afterwards).
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
	// Prompts can contain member identifiers and amounts. Keep them owner-only
	// even though they are deleted after the run (or retained under KeepPrompts).
	_ = os.Chmod(name, 0o600)
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		os.Remove(name)
		return "", func() {}, fmt.Errorf("write prompt: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", func() {}, err
	}
	// F-04: cleanup is always registered. Whether it removes the file is
	// gated by KeepPrompts so callers that want audit trail material can
	// opt in. Idempotent across double-defer / panic paths.
	cleanup := func() {
		if r.KeepPrompts {
			return
		}
		_ = os.Remove(name)
	}
	return name, cleanup, nil
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
//
// The emitting symbol has been renamed repeatedly upstream — `llama_print_timings:`
// (legacy), `llama_perf_context_print:`, and `common_perf_print:` on the b10580
// build we pin — and newer builds also prepend a timestamp and log level. The
// gate therefore matches on "perf" or "print_timings" anywhere in the prefix
// rather than on any single symbol name; note that `llama_perf_context_print`
// does NOT contain the literal "perf_print". Being too strict here silently
// reports 0 tok/s, understating a scored ADTC metric. Over-matching the prefix
// is harmless because the body switch below is exact.
func parsePerf(stderr string, out *Result) {
	for _, raw := range strings.Split(stderr, "\n") {
		line := strings.TrimSpace(raw)
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		prefix := line[:colon]
		if !strings.Contains(prefix, "perf") &&
			!strings.Contains(prefix, "print_timings") {
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
