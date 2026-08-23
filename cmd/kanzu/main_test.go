package main

import (
	"reflect"
	"testing"
)

// TestSplitArgs pins the CLI argument handling. The regressions guarded here
// both shipped in the usage text as working examples:
//
//	kanzu -lang sw ask "..."   -> reported: unknown command "sw"
//	kanzu ask "..." -lang sw   -> "-lang sw" folded into the request text
func TestSplitArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCmd  string
		wantRest []string
	}{
		{
			name:     "flag with value before subcommand",
			args:     []string{"-lang", "sw", "ask", "chunguza miamala"},
			wantCmd:  "ask",
			wantRest: []string{"-lang", "sw", "chunguza miamala"},
		},
		{
			name:     "flag with value after subcommand",
			args:     []string{"ask", "chunguza miamala", "-lang", "sw"},
			wantCmd:  "ask",
			wantRest: []string{"-lang", "sw", "chunguza miamala"},
		},
		{
			name:     "equals form needs no lookahead",
			args:     []string{"ask", "-lang=sw", "hello"},
			wantCmd:  "ask",
			wantRest: []string{"-lang=sw", "hello"},
		},
		{
			name:     "boolean flag must not eat the subcommand",
			args:     []string{"-no-model", "chat"},
			wantCmd:  "chat",
			wantRest: []string{"-no-model"},
		},
		{
			name:     "boolean flag after subcommand",
			args:     []string{"chat", "-no-model"},
			wantCmd:  "chat",
			wantRest: []string{"-no-model"},
		},
		{
			name:     "bare subcommand",
			args:     []string{"doctor"},
			wantCmd:  "doctor",
			wantRest: nil,
		},
		{
			name:     "numeric flag value",
			args:     []string{"scan", "-days", "7"},
			wantCmd:  "scan",
			wantRest: []string{"-days", "7"},
		},
		{
			name:     "multiple flags both sides",
			args:     []string{"-threads", "4", "report", "-days", "30", "-no-model"},
			wantCmd:  "report",
			wantRest: []string{"-threads", "4", "-days", "30", "-no-model"},
		},
		{
			name:     "policy key and value stay positional",
			args:     []string{"policy", "ugx_cash_threshold", "28000000"},
			wantCmd:  "policy",
			wantRest: []string{"ugx_cash_threshold", "28000000"},
		},
		{
			name:     "no subcommand, flags only",
			args:     []string{"-lang", "sw"},
			wantCmd:  "",
			wantRest: []string{"-lang", "sw"},
		},
		{
			name:     "member flag with value",
			args:     []string{"-member", "M-001", "scan"},
			wantCmd:  "scan",
			wantRest: []string{"-member", "M-001"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCmd, gotRest := splitArgs(tc.args)
			if gotCmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", gotCmd, tc.wantCmd)
			}
			if len(gotRest) == 0 && len(tc.wantRest) == 0 {
				return
			}
			if !reflect.DeepEqual(gotRest, tc.wantRest) {
				t.Errorf("rest = %#v, want %#v", gotRest, tc.wantRest)
			}
		})
	}
}

// TestSplitArgsDoesNotMutateInput guards against the append-aliasing bug the
// previous implementation was prone to.
func TestSplitArgsDoesNotMutateInput(t *testing.T) {
	args := []string{"-lang", "sw", "ask", "hello"}
	original := append([]string{}, args...)

	splitArgs(args)

	if !reflect.DeepEqual(args, original) {
		t.Errorf("splitArgs mutated its input: %#v, want %#v", args, original)
	}
}
