package llm

import (
	"os"
	"testing"
)

// Real stderr from the pinned b10580 build (common_perf_print, timestamped).
// Regression guard: an earlier prefix gate only accepted `llama_perf` /
// `llama_print_timings`, so every line here was skipped and Kanzu reported
// 0.0 tok/s — which would have understated a scored ADTC metric.
const b10580Stderr = `
0.08.700.664 I common_perf_print:    sampling time =       7.45 ms
0.08.700.692 I common_perf_print:    samplers time =       2.63 ms /    17 tokens
0.08.700.696 I common_perf_print:        load time =    4924.25 ms
0.08.700.701 I common_perf_print: prompt eval time =     487.36 ms /     9 tokens (   54.15 ms per token,    18.47 tokens per second)
0.08.700.706 I common_perf_print:        eval time =    1992.43 ms /     7 runs   (  284.63 ms per token,     3.51 tokens per second)
0.08.700.712 I common_perf_print:       total time =    2733.40 ms /    16 tokens
0.08.700.718 I common_perf_print: unaccounted time =     246.12 ms /   9.0 %
`

// Legacy format must keep working.
const legacyStderr = `
llama_print_timings:        load time =    1000.00 ms
llama_print_timings: prompt eval time =     500.00 ms /   512 tokens (    0.98 ms per token,  1024.00 tokens per second)
llama_print_timings:        eval time =    9800.00 ms /   128 runs   (   76.56 ms per token,    13.06 tokens per second)
`

// Intermediate llama_perf_context_print format.
const perfContextStderr = `
llama_perf_context_print:        load time =    2000.00 ms
llama_perf_context_print: prompt eval time =     250.00 ms /    64 tokens (    3.91 ms per token,   256.00 tokens per second)
llama_perf_context_print:        eval time =    5000.00 ms /    50 runs   (  100.00 ms per token,    10.00 tokens per second)
`

func TestParsePerfAcrossLlamaCppVersions(t *testing.T) {
	cases := []struct {
		name          string
		stderr        string
		wantLoadMS    float64
		wantPromptTok int
		wantGenTok    int
		wantGenTPS    float64
		wantPromptTPS float64
	}{
		{"b10580_common_perf_print", b10580Stderr, 4924.25, 9, 7, 3.51, 18.47},
		{"legacy_print_timings", legacyStderr, 1000, 512, 128, 13.06, 1024},
		{"perf_context_print", perfContextStderr, 2000, 64, 50, 10.00, 256},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got Result
			parsePerf(tc.stderr, &got)

			if got.LoadMS != tc.wantLoadMS {
				t.Errorf("LoadMS = %v, want %v", got.LoadMS, tc.wantLoadMS)
			}
			if got.PromptTokens != tc.wantPromptTok {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, tc.wantPromptTok)
			}
			if got.GeneratedTokens != tc.wantGenTok {
				t.Errorf("GeneratedTokens = %d, want %d", got.GeneratedTokens, tc.wantGenTok)
			}
			if got.GenerationTPS != tc.wantGenTPS {
				t.Errorf("GenerationTPS = %v, want %v", got.GenerationTPS, tc.wantGenTPS)
			}
			if got.PromptTPS != tc.wantPromptTPS {
				t.Errorf("PromptTPS = %v, want %v", got.PromptTPS, tc.wantPromptTPS)
			}
		})
	}
}

// TestParsePerfDoesNotConfusePromptEvalWithEval is the substring trap:
// "prompt eval time" contains "eval time", so a naive matcher would report
// prompt-processing speed (fast) as generation speed (slow).
func TestParsePerfDoesNotConfusePromptEvalWithEval(t *testing.T) {
	var got Result
	parsePerf(legacyStderr, &got)

	if got.GenerationTPS != 13.06 {
		t.Fatalf("GenerationTPS = %v, want 13.06 (not the 1024 prompt rate)", got.GenerationTPS)
	}
	if got.PromptEvalMS != 500 {
		t.Errorf("PromptEvalMS = %v, want 500", got.PromptEvalMS)
	}
	if got.EvalMS != 9800 {
		t.Errorf("EvalMS = %v, want 9800", got.EvalMS)
	}
}

// TestParsePerfDerivesTPSWhenRateAbsent covers builds that omit the
// parenthesised "tokens per second" figure.
func TestParsePerfDerivesTPSWhenRateAbsent(t *testing.T) {
	const noRate = `
llama_print_timings:        eval time =    2000.00 ms /    40 runs
`
	var got Result
	parsePerf(noRate, &got)

	if got.GeneratedTokens != 40 {
		t.Fatalf("GeneratedTokens = %d, want 40", got.GeneratedTokens)
	}
	// 40 tokens / 2.0 s = 20 tok/s
	if got.GenerationTPS != 20 {
		t.Errorf("GenerationTPS = %v, want 20 (derived)", got.GenerationTPS)
	}
}

// TestParsePerfIgnoresUnrelatedLines keeps stray log noise out of the numbers.
func TestParsePerfIgnoresUnrelatedLines(t *testing.T) {
	const noise = `
0.03.134.131 W load: control-looking token: 128247 '</s>' was not control-type
0.15.498.767 I  - Press Ctrl+C to interject at any time.
build: 10580 (54ee5ee64) with GNU 11.4.0 for Linux x86_64
`
	var got Result
	parsePerf(noise, &got)

	if got.LoadMS != 0 || got.GeneratedTokens != 0 || got.GenerationTPS != 0 {
		t.Errorf("unrelated lines produced numbers: %+v", got)
	}
}

// TestWritePromptCleanupNotKeep verifies the default behaviour: a successful
// writePrompt returns a cleanup that removes the file. F-04 regression —
// pre-fix, the cleanup was conditional and a write failure could leak the
// temp file into os.TempDir().
func TestWritePromptCleanupNotKeep(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{TmpDir: dir, KeepPrompts: false}
	name, cleanup, err := r.writePrompt("hello")
	if err != nil {
		t.Fatalf("writePrompt: %v", err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatalf("expected file to exist right after write: %v", err)
	}
	cleanup()
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("expected file removed after cleanup, stat err = %v", err)
	}
}

// TestWritePromptCleanupKeep verifies the audit/demo opt-in: when KeepPrompts
// is true the file stays on disk after cleanup.
func TestWritePromptCleanupKeep(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{TmpDir: dir, KeepPrompts: true}
	name, cleanup, err := r.writePrompt("hello")
	if err != nil {
		t.Fatalf("writePrompt: %v", err)
	}
	cleanup()
	if _, err := os.Stat(name); err != nil {
		t.Errorf("expected file retained under KeepPrompts: %v", err)
	}
}
