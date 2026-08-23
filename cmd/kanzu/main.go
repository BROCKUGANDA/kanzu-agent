// Command kanzu is the Kanzu Agent command-line interface.
//
// Every subcommand runs entirely against local files: one SQLite database, one
// GGUF weight file, and the markdown corpus in knowledge/. There is no network
// client anywhere in this program. Verify that claim mechanically with
// scripts/verify_offline.sh, which runs the agent inside a network namespace that
// has no interfaces at all.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kanzu-agent/kanzu/internal/agent"
	"github.com/kanzu-agent/kanzu/internal/config"
	"github.com/kanzu-agent/kanzu/internal/gguf"
	"github.com/kanzu-agent/kanzu/internal/i18n"
	"github.com/kanzu-agent/kanzu/internal/ledger"
	"github.com/kanzu-agent/kanzu/internal/llm"
	"github.com/kanzu-agent/kanzu/internal/rag"
	"github.com/kanzu-agent/kanzu/internal/rules"
	"github.com/kanzu-agent/kanzu/internal/seed"
	"github.com/kanzu-agent/kanzu/internal/thermal"
	"github.com/kanzu-agent/kanzu/internal/tui"
)

const usage = `kanzu — offline compliance copilot for savings groups and micro-SMEs

usage: kanzu [global flags] <command> [command flags]
       kanzu <command> [global flags] [command flags]

global flags (also available as KANZU_* environment variables):
  -lang en|sw|lg    Operator language (default en)
  -planner rules|hybrid  Plan source; hybrid lets the model refine a validated plan
  -threads N           llama.cpp thread count (default: cores-1, capped at 4)
  -db PATH             Ledger location

commands:
  init                 Build the local ledger from fixtures/ and index knowledge/
  chat                 Interactive terminal session (Bubble Tea TUI)
  ask   "<request>"    Run one request and exit
  scan                 Run the deterministic rule engine only (no model)
  report               Draft a suspicious-activity note for a window
  send  "<message>"    Enqueue an inbound request (offline queue)
  inbox                Process the inbound queue in paced batches
  outbox               Show replies awaiting a network link
  policy               Show or set institutional thresholds
  doctor               Verify model, ledger, corpus and llama.cpp wiring
  bench                Measure end-to-end agent throughput locally
  version              Print version information

examples:
  kanzu init
  kanzu ask "flag suspicious transactions this week and draft a compliance note"
  kanzu -lang sw ask "chunguza miamala ya kutiliwa shaka wiki hii"
  kanzu ask "chunguza miamala" -lang sw
  kanzu scan -days 7
  kanzu -lang lg report -days 14
`

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "kanzu: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return nil
	}

	// Global flags may appear before or after the subcommand.
	var (
		lang    = flag.String("lang", "", "operator language: en or sw")
		planner = flag.String("planner", "", "plan source: rules or hybrid")
		threads = flag.Int("threads", 0, "llama.cpp thread count")
		dbPath  = flag.String("db", "", "ledger path")
		days    = flag.Int("days", 0, "window length in days")
		member  = flag.String("member", "", "restrict to one member id or name")
		limit   = flag.Int("limit", 5, "batch size for inbox/outbox")
		peer    = flag.String("from", "", "officer reference for send")
		noModel = flag.Bool("no-model", false, "skip inference; deterministic output only")
		reps    = flag.Int("reps", 3, "repetitions for bench")
	)

	cmd, rest := splitArgs(os.Args[1:])
	flag.CommandLine.SetOutput(os.Stderr)
	if err := flag.CommandLine.Parse(rest); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if cmd == "version" {
		return cmdVersion()
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *lang != "" {
		cfg.Lang = *lang
	}
	if *planner != "" {
		cfg.Planner = *planner
	}
	if *threads > 0 {
		cfg.Threads = *threads
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
		if !filepath.IsAbs(cfg.DBPath) {
			cfg.DBPath = filepath.Join(cfg.RepoRoot, cfg.DBPath)
		}
	}

	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "init":
		return cmdInit(ctx, cfg)
	case "doctor":
		return cmdDoctor(ctx, cfg)
	case "chat":
		return cmdChat(ctx, cfg, *noModel)
	case "ask":
		return cmdAsk(ctx, cfg, strings.Join(flag.Args(), " "), *noModel)
	case "scan":
		return cmdScan(ctx, cfg, *days, *member)
	case "report":
		return cmdReport(ctx, cfg, *days, *member, *noModel)
	case "send":
		return cmdSend(ctx, cfg, strings.Join(flag.Args(), " "), *peer)
	case "inbox":
		return cmdInbox(ctx, cfg, *limit, *noModel)
	case "outbox":
		return cmdOutbox(ctx, cfg, *limit)
	case "policy":
		return cmdPolicy(ctx, cfg, flag.Args())
	case "bench":
		return cmdBench(ctx, cfg, *reps)
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// valueFlags are the flags that consume the following argument when written in
// the "-flag value" form. splitArgs has to know about them: otherwise
// "kanzu -lang sw ask ..." mistakes the flag's value ("sw") for the subcommand.
var valueFlags = map[string]bool{
	"lang": true, "planner": true, "threads": true, "db": true,
	"days": true, "member": true, "limit": true, "from": true, "reps": true,
}

// splitArgs separates the subcommand from its flags, tolerating either order.
//
// Two things make this fiddly. First, a value-taking flag written as
// "-lang sw" contributes a bare token that must not be read as the subcommand.
// Second, Go's flag package stops parsing at the first non-flag argument, so
// every flag has to be moved ahead of the positional arguments before
// flag.Parse sees them — otherwise "kanzu ask "..." -lang sw" silently folds
// "-lang sw" into the request text instead of switching language.
func splitArgs(args []string) (string, []string) {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			// "-flag=value" already carries its value.
			if !strings.Contains(name, "=") && valueFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}

	if len(positional) == 0 {
		return "", flags
	}
	return positional[0], append(flags, positional[1:]...)
}

// ── shared wiring ────────────────────────────────────────────────────────────

type stack struct {
	cfg    *config.Config
	db     *ledger.DB
	kb     *rag.Index
	engine *rules.Engine
	gov    *thermal.Governor
	runner *llm.Runner
	agent  *agent.Agent
}

func (s *stack) Close() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// open wires the whole stack. withModel=false skips llama.cpp resolution entirely,
// which is what makes the deterministic paths usable on a machine that has no
// weights and no llama.cpp build.
func open(ctx context.Context, cfg *config.Config, withModel bool) (*stack, error) {
	if err := cfg.EnsureStateDir(); err != nil {
		return nil, err
	}
	db, err := ledger.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	s := &stack{cfg: cfg, db: db, kb: rag.New(db.SQL())}

	engine, err := rules.NewEngine(ctx, db)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.engine = engine
	s.gov = thermal.New(cfg.ThermalCeilingC, cfg.ThermalResumeC, cfg.DutyCycle)

	if withModel {
		if err := cfg.RequireModel(); err != nil {
			fmt.Fprintf(os.Stderr, "note: %v\n", err)
		} else if bin, err := llm.Resolve(cfg.LlamaCLI); err != nil {
			fmt.Fprintf(os.Stderr, "note: %v\n", err)
		} else {
			s.runner = llm.New(bin, cfg.ModelPath,
				filepath.Join(filepath.Dir(cfg.DBPath), "prompts"),
				cfg.Threads, cfg.ContextTokens, s.gov)
		}
	}

	s.agent = agent.New(cfg, db, s.kb, engine, s.runner, s.gov)
	return s, nil
}

// ── commands ─────────────────────────────────────────────────────────────────

func cmdVersion() error {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("kanzu (version unknown: metadata.json not found)")
		return nil
	}
	fmt.Printf("kanzu %s\n", orUnknown(cfg.Meta.Kanzu.AppVersion))
	fmt.Printf("  model    %s (%s, %s)\n", cfg.Meta.Model.Name,
		cfg.Meta.Model.Quantization, cfg.Meta.Model.ParamsEstimate)
	fmt.Printf("  runtime  %s\n", cfg.Meta.Model.Runtime)
	fmt.Printf("  team     %s · domain %s · languages %s\n",
		cfg.Meta.TeamID, cfg.Meta.Domain, strings.Join(cfg.Meta.LanguageScope, ","))
	fmt.Printf("  network  none at runtime\n")
	return nil
}

func cmdInit(ctx context.Context, cfg *config.Config) error {
	s, err := open(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer s.Close()

	fmt.Printf("ledger    %s\n", s.db.Path())

	rep, err := seed.Load(ctx, s.db, cfg.FixturesDir, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("fixtures  %d members · %d accounts · %d transactions (anchored %s)\n",
		rep.Members, rep.Accounts, rep.Transactions, rep.Anchor.Format("2006-01-02"))

	chunks, err := s.kb.Ingest(ctx, cfg.KnowledgeDir)
	if err != nil {
		return err
	}
	byLang, err := s.kb.Stats(ctx)
	if err != nil {
		return err
	}
	parts := make([]string, 0, len(byLang))
	for l, n := range byLang {
		parts = append(parts, fmt.Sprintf("%s=%d", l, n))
	}
	fmt.Printf("corpus    %d chunks indexed (%s)\n", chunks, strings.Join(parts, " "))

	fmt.Printf("\nready. try:\n  kanzu ask \"flag suspicious transactions this week and draft a compliance note\"\n")
	return nil
}

func cmdDoctor(ctx context.Context, cfg *config.Config) error {
	s, err := open(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer s.Close()

	ok := func(b bool) string {
		if b {
			return "ok  "
		}
		return "FAIL"
	}

	fmt.Printf("repo root      %s\n", cfg.RepoRoot)
	fmt.Printf("metadata       %s\n", cfg.MetadataPath)
	fmt.Printf("ledger         %s\n", s.db.Path())
	fmt.Println()

	// Model file and header, mirroring what adtc-profiler will compute.
	fmt.Println("── model ──")
	modelErr := cfg.RequireModel()
	fmt.Printf("[%s] weights present   %s\n", ok(modelErr == nil), cfg.ModelPath)
	if modelErr != nil {
		fmt.Printf("       %v\n", modelErr)
	} else {
		info, err := gguf.Read(cfg.ModelPath)
		if err != nil {
			fmt.Printf("[FAIL] gguf header      %v\n", err)
		} else {
			claimed := cfg.Meta.Model.ParamsEstimate
			match, checkable := gguf.FraudCheck(claimed, info.ParamsCount)
			fmt.Printf("[ok  ] gguf v%d          arch=%s quant=%s ctx=%d tensors=%d\n",
				info.Version, info.Architecture, info.Quantization, info.ContextLength, info.TensorCount)
			fmt.Printf("[%s] params_match      measured=%d claimed=%s (checkable=%v)\n",
				ok(match), info.ParamsCount, claimed, checkable)
			if !match && checkable {
				fmt.Printf("       adtc-profiler would report params_match=false. Set\n")
				fmt.Printf("       model.parameters_estimate to %s in metadata.json.\n",
					gguf.SuggestEstimate(info.ParamsCount))
			}
			archOK := strings.EqualFold(info.Architecture, cfg.Meta.Runtime.Arch)
			fmt.Printf("[%s] architecture      %s (metadata declares %s)\n",
				ok(archOK), info.Architecture, cfg.Meta.Runtime.Arch)
		}
	}

	// llama.cpp
	fmt.Println("\n── llama.cpp ──")
	bin, binErr := llm.Resolve(cfg.LlamaCLI)
	fmt.Printf("[%s] binary            %s\n", ok(binErr == nil), bin)
	if binErr != nil {
		fmt.Printf("       %v\n", binErr)
	} else {
		r := llm.New(bin, cfg.ModelPath, filepath.Join(filepath.Dir(cfg.DBPath), "prompts"),
			cfg.Threads, cfg.ContextTokens, s.gov)
		ver, err := r.Probe()
		fmt.Printf("[%s] responds          %s\n", ok(err == nil), ver)
		if err != nil {
			fmt.Printf("       %v\n", err)
		}
	}

	// Data
	fmt.Println("\n── local data ──")
	counts, err := s.db.Counts(ctx)
	if err != nil {
		return err
	}
	for _, t := range []string{"members", "accounts", "transactions", "kb_chunks", "alerts", "cases", "messages", "audit_log"} {
		fmt.Printf("[%s] %-16s %d rows\n", ok(counts[t] > 0 || t == "alerts" || t == "cases" || t == "messages"), t, counts[t])
	}
	if counts["transactions"] == 0 || counts["kb_chunks"] == 0 {
		fmt.Printf("\n       ledger or corpus is empty — run: kanzu init\n")
	}

	// Retrieval smoke test in both languages: the cross-lingual path is the
	// african_alpha claim, so a broken lexicon must surface here.
	fmt.Println("\n── retrieval (cross-language) ──")
	for _, probe := range []struct {
		q    string
		lang i18n.Lang
	}{
		{"structuring deposits below reporting threshold", i18n.EN},
		{"kugawanya amana chini ya kiwango cha kuripoti", i18n.SW},
		{"okugabanya obutunzi wansi w'omugereka", i18n.LG},
	} {
		chunks, err := s.kb.Retrieve(ctx, probe.q, probe.lang, 2)
		if err != nil {
			fmt.Printf("[FAIL] %s  %v\n", probe.lang, err)
			continue
		}
		langs := make([]string, 0, len(chunks))
		for _, c := range chunks {
			langs = append(langs, c.Lang)
		}
		fmt.Printf("[%s] %s query          %d hits [%s]\n", ok(len(chunks) > 0), probe.lang,
			len(chunks), strings.Join(langs, " "))
	}

	// Thermal
	fmt.Println("\n── thermal ──")
	if temp, sensorOK := thermal.ReadHottestC(); sensorOK {
		fmt.Printf("[ok  ] sensor            %.1f°C (ceiling %.0f°C, resume %.0f°C)\n",
			temp, cfg.ThermalCeilingC, cfg.ThermalResumeC)
	} else {
		fmt.Printf("[warn] sensor            not readable on this platform; duty cycling still applies\n")
	}
	fmt.Printf("       threads           %d · duty cycle %.0f%%\n", cfg.Threads, cfg.DutyCycle*100)

	// Prompt templates
	fmt.Println("\n── prompt templates ──")
	promptChecks := cfg.CheckPromptFiles()
	// Print in a stable order.
	for _, name := range []string{"planning-en", "planning-sw", "planning-lg", "narration-en", "narration-sw", "narration-lg"} {
		err := promptChecks[name]
		fmt.Printf("[%s] %-16s  %s\n", ok(err == nil), name, cfg.PromptFiles()[name])
		if err != nil {
			fmt.Printf("       %v\n", err)
		}
	}

	fmt.Println("\n── network ──")
	fmt.Printf("[ok  ] runtime calls     none (verify: bash scripts/verify_offline.sh)\n")
	return nil
}

func cmdChat(ctx context.Context, cfg *config.Config, noModel bool) error {
	s, err := open(ctx, cfg, !noModel)
	if err != nil {
		return err
	}
	defer s.Close()

	// runAgent is the bridge between the TUI and the real agent layer.
	// The TUI has no knowledge of SQL, the model, or AML rules; it only
	// calls this function and renders the returned Outcome.
	runAgent := func(reqCtx context.Context, request string, lang i18n.Lang) (*agent.Outcome, error) {
		s.agent.Lang = lang
		return s.agent.Execute(reqCtx, request)
	}

	program := tea.NewProgram(
		tui.NewApp(runAgent, sectionProviders(s, cfg)),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err = program.Run()
	return err
}

// sectionProviders builds the read-only snapshots the TUI's non-chat sections
// render. All SQL, rule evaluation and corpus access stays on this side of the
// boundary; the TUI only formats what it is handed.
func sectionProviders(s *stack, cfg *config.Config) tui.Providers {
	lang := i18n.Parse(cfg.Lang)

	window := func(days int) (time.Time, time.Time) {
		now := time.Now().UTC()
		end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		return end.AddDate(0, 0, -days), end
	}

	return tui.Providers{
		// Quick Scan — deterministic rule engine, no model.
		Scan: func(ctx context.Context, days int) ([]tui.AlertRow, error) {
			start, end := window(days)
			findings, err := s.engine.Scan(ctx, start, end, nil)
			if err != nil {
				return nil, err
			}
			out := make([]tui.AlertRow, 0, len(findings))
			for _, f := range findings {
				out = append(out, tui.AlertRow{
					RuleID:   f.RuleID,
					Title:    rules.Title(f.RuleID, lang),
					Member:   f.MemberName,
					MemberID: f.MemberID,
					Severity: f.Severity,
					Detail:   f.Describe(lang),
					Txns:     strings.Join(f.TxnIDs, ","),
				})
			}
			return out, nil
		},

		// Reports — draft suspicious-activity files.
		Cases: func(ctx context.Context) ([]tui.CaseRow, error) {
			cases, err := s.db.RecentCases(ctx, 100)
			if err != nil {
				return nil, err
			}
			names := map[string]string{}
			out := make([]tui.CaseRow, 0, len(cases))
			for _, c := range cases {
				name, ok := names[c.MemberID]
				if !ok {
					if m, err := s.db.Member(ctx, c.MemberID); err == nil {
						name = m.Name
					} else {
						name = c.MemberID
					}
					names[c.MemberID] = name
				}
				out = append(out, tui.CaseRow{
					ID:     c.ID,
					Title:  c.Title,
					Member: name,
					Lang:   c.Lang,
					Status: c.Status,
					Opened: c.OpenedAt.Format("2006-01-02"),
				})
			}
			return out, nil
		},

		// Members — local register.
		Members: func(ctx context.Context) ([]tui.MemberRow, error) {
			members, err := s.db.Members(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]tui.MemberRow, 0, len(members))
			for _, m := range members {
				out = append(out, tui.MemberRow{
					ID:       m.ID,
					Name:     m.Name,
					Branch:   m.HomeBranch,
					RiskBand: m.RiskBand,
					KYCLevel: m.KYCLevel,
					PEP:      m.IsPEP,
					Dormant:  m.Dormant,
				})
			}
			return out, nil
		},

		// Inbox Queue — offline message queue, both directions.
		Inbox: func(ctx context.Context) ([]tui.InboxRow, error) {
			inbound, err := s.db.PendingInbound(ctx, 50)
			if err != nil {
				return nil, err
			}
			outbound, err := s.db.OutboundQueue(ctx, 50)
			if err != nil {
				return nil, err
			}
			out := make([]tui.InboxRow, 0, len(inbound)+len(outbound))
			for _, m := range append(inbound, outbound...) {
				out = append(out, tui.InboxRow{
					ID:        m.ID,
					Direction: m.Direction,
					Peer:      m.Peer,
					Lang:      m.Lang,
					Body:      m.Body,
					Status:    m.Status,
					Created:   m.CreatedAt.Format("2006-01-02 15:04"),
				})
			}
			return out, nil
		},

		// Knowledge Base — indexed chunks per language.
		KB: func(ctx context.Context) ([]tui.KBRow, error) {
			byLang, err := s.kb.Stats(ctx)
			if err != nil {
				return nil, err
			}
			langs := make([]string, 0, len(byLang))
			for l := range byLang {
				langs = append(langs, l)
			}
			sortStrings(langs)
			out := make([]tui.KBRow, 0, len(langs))
			for _, l := range langs {
				out = append(out, tui.KBRow{Lang: l, Chunks: byLang[l]})
			}
			return out, nil
		},

		// Policy — institutional thresholds.
		Policy: func(ctx context.Context) ([]tui.PolicyRow, error) {
			p, err := s.db.LoadPolicy(ctx)
			if err != nil {
				return nil, err
			}
			keys := make([]string, 0, len(p))
			for k := range p {
				keys = append(keys, k)
			}
			sortStrings(keys)
			out := make([]tui.PolicyRow, 0, len(keys))
			for _, k := range keys {
				out = append(out, tui.PolicyRow{Key: k, Value: p[k]})
			}
			return out, nil
		},

		// Doctor — same checks cmdDoctor prints, as rows.
		Doctor: func(ctx context.Context) ([]tui.DoctorRow, error) {
			return doctorRows(ctx, cfg, s), nil
		},
	}
}

// doctorRows mirrors cmdDoctor's checks in a form the TUI can render.
func doctorRows(ctx context.Context, cfg *config.Config, s *stack) []tui.DoctorRow {
	state := func(ok bool) string {
		if ok {
			return "ok"
		}
		return "fail"
	}
	rows := []tui.DoctorRow{}

	// Model + GGUF header.
	modelErr := cfg.RequireModel()
	rows = append(rows, tui.DoctorRow{
		Label: "weights", State: state(modelErr == nil), Detail: cfg.ModelPath,
	})
	if modelErr == nil {
		if info, err := gguf.Read(cfg.ModelPath); err != nil {
			rows = append(rows, tui.DoctorRow{Label: "gguf header", State: "fail", Detail: err.Error()})
		} else {
			claimed := cfg.Meta.Model.ParamsEstimate
			match, checkable := gguf.FraudCheck(claimed, info.ParamsCount)
			rows = append(rows, tui.DoctorRow{
				Label: "gguf", State: "ok",
				Detail: fmt.Sprintf("v%d arch=%s quant=%s ctx=%d tensors=%d",
					info.Version, info.Architecture, info.Quantization,
					info.ContextLength, info.TensorCount),
			})
			rows = append(rows, tui.DoctorRow{
				Label: "params_match", State: state(match || !checkable),
				Detail: fmt.Sprintf("measured=%d claimed=%s", info.ParamsCount, claimed),
			})
			rows = append(rows, tui.DoctorRow{
				Label: "architecture",
				State: state(strings.EqualFold(info.Architecture, cfg.Meta.Runtime.Arch)),
				Detail: fmt.Sprintf("%s (metadata declares %s)",
					info.Architecture, cfg.Meta.Runtime.Arch),
			})
		}
	}

	// llama.cpp
	bin, binErr := llm.Resolve(cfg.LlamaCLI)
	detail := bin
	if binErr != nil {
		detail = binErr.Error()
	}
	rows = append(rows, tui.DoctorRow{Label: "llama.cpp", State: state(binErr == nil), Detail: detail})

	// Ledger counts.
	if counts, err := s.db.Counts(ctx); err == nil {
		for _, t := range []string{"members", "accounts", "transactions", "kb_chunks", "alerts", "cases"} {
			optional := t == "alerts" || t == "cases"
			rows = append(rows, tui.DoctorRow{
				Label: t, State: state(counts[t] > 0 || optional),
				Detail: fmt.Sprintf("%d rows", counts[t]),
			})
		}
	} else {
		rows = append(rows, tui.DoctorRow{Label: "ledger", State: "fail", Detail: err.Error()})
	}

	// Cross-language retrieval — the african_alpha claim gate.
	for _, probe := range []struct {
		q    string
		lang i18n.Lang
	}{
		{"structuring deposits below reporting threshold", i18n.EN},
		{"kugawanya amana chini ya kiwango cha kuripoti", i18n.SW},
		{"okugabanya obutunzi wansi w'omugereka", i18n.LG},
	} {
		chunks, err := s.kb.Retrieve(ctx, probe.q, probe.lang, 2)
		if err != nil {
			rows = append(rows, tui.DoctorRow{
				Label: "retrieval " + string(probe.lang), State: "fail", Detail: err.Error(),
			})
			continue
		}
		langs := make([]string, 0, len(chunks))
		for _, c := range chunks {
			langs = append(langs, c.Lang)
		}
		rows = append(rows, tui.DoctorRow{
			Label: "retrieval " + string(probe.lang), State: state(len(chunks) > 0),
			Detail: fmt.Sprintf("%d hits [%s]", len(chunks), strings.Join(langs, " ")),
		})
	}

	// Thermal sensor is advisory only.
	if temp, sensorOK := thermal.ReadHottestC(); sensorOK {
		rows = append(rows, tui.DoctorRow{
			Label: "thermal", State: "ok",
			Detail: fmt.Sprintf("%.1f°C (ceiling %.0f°C)", temp, cfg.ThermalCeilingC),
		})
	} else {
		rows = append(rows, tui.DoctorRow{
			Label: "thermal", State: "warn",
			Detail: "sensor not readable on this platform; duty cycling still applies",
		})
	}

	// Prompt templates.
	checks := cfg.CheckPromptFiles()
	for _, name := range []string{"planning-en", "planning-sw", "planning-lg",
		"narration-en", "narration-sw", "narration-lg"} {
		err := checks[name]
		d := "present"
		if err != nil {
			d = err.Error()
		}
		rows = append(rows, tui.DoctorRow{Label: name, State: state(err == nil), Detail: d})
	}

	rows = append(rows, tui.DoctorRow{
		Label: "network", State: "ok", Detail: "no runtime calls",
	})
	return rows
}

func cmdAsk(ctx context.Context, cfg *config.Config, request string, noModel bool) error {
	if strings.TrimSpace(request) == "" {
		return errors.New(`ask needs a request, e.g. kanzu ask "flag suspicious transactions this week"`)
	}
	s, err := open(ctx, cfg, !noModel)
	if err != nil {
		return err
	}
	defer s.Close()
	return tui.New(s.agent, os.Stdin, os.Stdout).Ask(ctx, request)
}

func cmdScan(ctx context.Context, cfg *config.Config, days int, member string) error {
	s, err := open(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer s.Close()

	if days <= 0 {
		days = 7
	}
	lang := i18n.Parse(cfg.Lang)
	now := time.Now().UTC()
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	start := end.AddDate(0, 0, -days)

	findings, err := s.engine.Scan(ctx, start, end, nil)
	if err != nil {
		return err
	}
	if member != "" {
		m, err := s.db.FindMember(ctx, member)
		if err != nil {
			return err
		}
		filtered := findings[:0:0]
		for _, f := range findings {
			if f.MemberID == m.ID {
				filtered = append(filtered, f)
			}
		}
		findings = filtered
	}

	fmt.Printf("window %s → %s · %d rule(s) evaluated · no model used\n\n",
		start.Format("2006-01-02"), end.AddDate(0, 0, -1).Format("2006-01-02"), len(rules.All()))
	if len(findings) == 0 {
		fmt.Println(i18n.T(lang, "alerts.none"))
		return nil
	}
	for _, f := range findings {
		fmt.Printf("[%-6s] %-34s %s (%s)\n", strings.ToUpper(f.Severity),
			rules.Title(f.RuleID, lang), f.MemberName, f.MemberID)
		fmt.Printf("          %s\n", f.Describe(lang))
		fmt.Printf("          rule=%s score=%.2f txns=%s\n\n", f.RuleID, f.Score, strings.Join(f.TxnIDs, ","))
	}
	fmt.Printf("%d %s\n", len(findings), i18n.T(lang, "alerts.count"))
	return nil
}

func cmdReport(ctx context.Context, cfg *config.Config, days int, member string, noModel bool) error {
	if days <= 0 {
		days = 7
	}
	request := fmt.Sprintf("draft a compliance note for the last %d days", days)
	if member != "" {
		request += " for member " + member
	}
	if i18n.Parse(cfg.Lang) == i18n.SW {
		request = fmt.Sprintf("andika taarifa ya uzingatiaji kwa siku %d zilizopita", days)
		if member != "" {
			request += " kwa mwanachama " + member
		}
	}
	return cmdAsk(ctx, cfg, request, noModel)
}

func cmdSend(ctx context.Context, cfg *config.Config, body, peer string) error {
	s, err := open(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer s.Close()

	id, err := tui.Enqueue(ctx, s.db, peer, body, "", "queue")
	if err != nil {
		return err
	}
	fmt.Printf("queued inbound message #%d — process with: kanzu inbox\n", id)
	return nil
}

func cmdInbox(ctx context.Context, cfg *config.Config, limit int, noModel bool) error {
	s, err := open(ctx, cfg, !noModel)
	if err != nil {
		return err
	}
	defer s.Close()
	return tui.ProcessQueue(ctx, s.agent, s.db, os.Stdout, limit)
}

func cmdOutbox(ctx context.Context, cfg *config.Config, limit int) error {
	s, err := open(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer s.Close()
	return tui.ShowOutbound(ctx, s.db, os.Stdout, limit)
}

func cmdPolicy(ctx context.Context, cfg *config.Config, args []string) error {
	s, err := open(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer s.Close()

	if len(args) >= 2 {
		if err := s.db.Set(ctx, "cli", args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("policy %s = %s\n", args[0], args[1])
		return nil
	}

	p, err := s.db.LoadPolicy(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sortStrings(keys)
	fmt.Print("institutional thresholds (set per SACCO; every alert records the value it fired against)\n\n")
	for _, k := range keys {
		fmt.Printf("  %-34s %s\n", k, p[k])
	}
	fmt.Printf("\nchange one with: kanzu policy <key> <value>\n")
	return nil
}

// cmdBench measures the whole agent, not just the model.
//
// This is deliberately separate from adtc-profiler, which measures llama-bench in
// isolation and is the number that gets scored. This one answers a different
// question the profiler cannot: how long an operator actually waits for a
// compliance note, including retrieval, rule evaluation and thermal pacing.
func cmdBench(ctx context.Context, cfg *config.Config, reps int) error {
	if reps < 1 {
		reps = 1
	}
	s, err := open(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer s.Close()
	if s.runner == nil {
		return errors.New("bench needs weights and llama.cpp; run kanzu doctor to diagnose")
	}

	requests := []string{
		"flag suspicious transactions this week and draft a compliance note",
		"chunguza miamala ya kutiliwa shaka wiki hii na andika taarifa",
	}

	fmt.Printf("threads=%d ctx=%d max_tokens=%d duty=%.0f%% planner=%s\n\n",
		cfg.Threads, cfg.ContextTokens, cfg.MaxTokens, cfg.DutyCycle*100, cfg.Planner)

	var totalTPS float64
	var samples int
	for r := 0; r < reps; r++ {
		for _, req := range requests {
			start := time.Now()
			out, err := s.agent.Execute(ctx, req)
			if err != nil {
				return err
			}
			wall := time.Since(start)
			tps := 0.0
			if out.Perf != nil {
				tps = out.Perf.GenerationTPS
				totalTPS += tps
				samples++
			}
			fmt.Printf("rep %d %-3s wall=%-7s gen=%5.1f tok/s findings=%d\n",
				r+1, out.Plan.Lang, wall.Round(100*time.Millisecond), tps, len(out.Evidence.Findings))
		}
	}

	if samples > 0 {
		fmt.Printf("\nmean generation throughput: %.2f tok/s over %d inference call(s)\n", totalTPS/float64(samples), samples)
		fmt.Printf("ADTC S_perf = min(TPS/15.0, 1.0)*100 = %.1f\n", minf(totalTPS/float64(samples)/15.0, 1.0)*100)
	}
	fmt.Printf("thermal: %s\n", benchThermal(s.gov))
	fmt.Printf("\nnote: the scored throughput number comes from adtc-profiler running\n")
	fmt.Printf("llama-bench in isolation. Run scripts/run_profiler.sh for that.\n")
	return nil
}

func benchThermal(g *thermal.Governor) string {
	st := g.Snapshot()
	if !st.SensorPresent {
		return fmt.Sprintf("no sensor · %d bursts · duty %.0f%%", st.Bursts, st.DutyAchieved*100)
	}
	return fmt.Sprintf("peak %.1f°C · %d bursts · %d gated pauses · duty %.0f%%",
		st.PeakTempC, st.Bursts, st.Pauses, st.DutyAchieved*100)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unversioned)"
	}
	return s
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
