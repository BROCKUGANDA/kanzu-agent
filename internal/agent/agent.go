// Package agent orchestrates Kanzu's planner, deterministic tools, retrieval and
// narration.
//
// The control flow is the point: deterministic tools run first and produce an
// Evidence record; the language model is invoked afterwards, once, with that
// Evidence in its context and an instruction that it may only describe what is
// already there. The model never decides whether something is suspicious, never
// picks a severity and never sees the raw ledger. If inference fails or the
// weights are missing, Execute still returns the full deterministic finding set,
// so the compliance function degrades to plain output rather than to nothing.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kanzu-agent/kanzu/internal/config"
	"github.com/kanzu-agent/kanzu/internal/i18n"
	"github.com/kanzu-agent/kanzu/internal/ledger"
	"github.com/kanzu-agent/kanzu/internal/llm"
	"github.com/kanzu-agent/kanzu/internal/rag"
	"github.com/kanzu-agent/kanzu/internal/rules"
	"github.com/kanzu-agent/kanzu/internal/thermal"
)

// Agent wires the layers together.
type Agent struct {
	Cfg       *config.Config
	DB        *ledger.DB
	KB        *rag.Index
	Engine    *rules.Engine
	LLM       *llm.Runner // nil when weights are absent
	Gov       *thermal.Governor
	Lang      i18n.Lang
	PromptDir string // path to var/prompts/ for versioned system prompts

	// Now is injectable so tests and the demo script get stable windows.
	Now func() time.Time

	lastPlanMu sync.Mutex
	lastPlan   Plan
}

// New constructs an Agent.
func New(cfg *config.Config, db *ledger.DB, kb *rag.Index, engine *rules.Engine,
	runner *llm.Runner, gov *thermal.Governor) *Agent {
	return &Agent{
		Cfg:       cfg,
		DB:        db,
		KB:        kb,
		Engine:    engine,
		LLM:       runner,
		Gov:       gov,
		Lang:      i18n.Parse(cfg.Lang),
		PromptDir: cfg.PromptDir,
		Now:       time.Now,
	}
}

// LedgerSummary aggregates a window without shipping every row to the model.
type LedgerSummary struct {
	TxnCount     int
	MemberCount  int
	CreditMinor  int64
	DebitMinor   int64
	Currency     string
	ByChannel    map[string]int
	LargestMinor int64
}

// Evidence is everything the deterministic layer established. It is the only
// factual input the model receives.
type Evidence struct {
	Window   Window
	Member   *ledger.Member
	Summary  LedgerSummary
	Findings []rules.Finding
	Chunks   []rag.Chunk
	Alerts   []ledger.Alert
	Executed []string
}

// Outcome is the result of one request.
type Outcome struct {
	Plan     Plan
	Evidence Evidence
	// Answer is the narrated text. Empty when no model was available; callers
	// fall back to Evidence, which always stands on its own.
	Answer     string
	Perf       *llm.Result
	CaseID     int64
	Degraded   bool
	DegradeWhy string
}

// LastPlan returns the most recent plan, for the `:plan` chat command.
// Execute runs in a goroutine while the TUI reads plans from the Bubble Tea
// update loop, so the field is guarded rather than read directly.
func (a *Agent) LastPlan() Plan {
	a.lastPlanMu.Lock()
	defer a.lastPlanMu.Unlock()
	return a.lastPlan
}

// Execute runs one request end to end.
func (a *Agent) Execute(ctx context.Context, request string) (*Outcome, error) {
	now := a.Now().UTC()

	plan := BuildPlan(request, now)
	// Honour an explicit operator language choice over lexical detection: if the
	// session is set to Kiswahili, an English-looking query still gets a
	// Kiswahili answer.
	if a.Lang != "" {
		plan.Lang = a.Lang
	}

	if a.Cfg.Planner != "rules" && a.LLM != nil && plan.Intent != IntentHelp {
		refined, notes := a.refinePlan(ctx, request, plan)
		plan = refined
		_ = notes
	}
	a.setLastPlan(plan)

	out := &Outcome{Plan: plan}
	ev, err := a.gather(ctx, plan)
	if err != nil {
		return nil, err
	}
	out.Evidence = ev

	needsNarration := plan.Intent == IntentReport || plan.Intent == IntentExplain ||
		plan.Intent == IntentProfile || plan.Intent == IntentScan
	if needsNarration {
		if a.LLM == nil {
			out.Degraded = true
			out.DegradeWhy = i18n.T(plan.Lang, "model.offline")
		} else {
			answer, perf, err := a.narrate(ctx, request, plan, ev)
			if err != nil {
				// Deterministic findings are the load-bearing output; an
				// inference failure must not discard them.
				out.Degraded = true
				out.DegradeWhy = err.Error()
			} else {
				out.Answer = answer
				out.Perf = perf
			}
		}
	}

	if plan.Intent == IntentReport && len(ev.Findings) > 0 {
		id, err := a.persistCase(ctx, plan, ev, out.Answer)
		if err != nil {
			return nil, err
		}
		out.CaseID = id
	}

	detail := fmt.Sprintf("intent=%s lang=%s window=%s findings=%d origin=%s",
		plan.Intent, plan.Lang, plan.Window.Label, len(ev.Findings), plan.Origin)
	if err := a.DB.Audit(ctx, "agent", "execute", truncate(request, 200), detail); err != nil {
		return nil, err
	}
	return out, nil
}

// gather executes the deterministic steps of a plan in order.
func (a *Agent) gather(ctx context.Context, plan Plan) (Evidence, error) {
	ev := Evidence{Window: plan.Window}

	for _, step := range plan.Steps {
		switch step.Tool {
		case ToolMemberLookup:
			ref := step.Args["member"]
			if ref == "" {
				ref = plan.Member
			}
			if ref == "" {
				continue
			}
			m, err := a.DB.FindMember(ctx, ref)
			if err != nil {
				ev.Executed = append(ev.Executed, step.Tool+" (no match: "+ref+")")
				continue
			}
			ev.Member = &m
			alerts, err := a.DB.AlertsForMember(ctx, m.ID)
			if err != nil {
				return ev, err
			}
			ev.Alerts = alerts
			ev.Executed = append(ev.Executed, step.Tool)

		case ToolLedgerQuery:
			var txns []ledger.Txn
			var err error
			if plan.Member != "" && ev.Member != nil {
				txns, err = a.DB.TxnsForMember(ctx, ev.Member.ID, plan.Window.Start, plan.Window.End)
			} else if plan.Member != "" {
				if m, e := a.DB.FindMember(ctx, plan.Member); e == nil {
					ev.Member = &m
					txns, err = a.DB.TxnsForMember(ctx, m.ID, plan.Window.Start, plan.Window.End)
				} else {
					txns, err = a.DB.TxnsBetween(ctx, plan.Window.Start, plan.Window.End)
				}
			} else {
				txns, err = a.DB.TxnsBetween(ctx, plan.Window.Start, plan.Window.End)
			}
			if err != nil {
				return ev, err
			}
			ev.Summary = summarise(txns, a.Engine.Policy.Str("currency", "UGX"))
			ev.Executed = append(ev.Executed, step.Tool)

		case ToolRulesScan:
			ruleIDs := []string{}
			if v := strings.TrimSpace(step.Args["rule_ids"]); v != "" && !strings.EqualFold(v, "ALL") {
				ruleIDs = strings.Split(v, ",")
			}
			findings, err := a.Engine.Scan(ctx, plan.Window.Start, plan.Window.End, ruleIDs)
			if err != nil {
				return ev, err
			}
			if ev.Member != nil {
				filtered := findings[:0:0]
				for _, f := range findings {
					if f.MemberID == ev.Member.ID {
						filtered = append(filtered, f)
					}
				}
				findings = filtered
			}
			ev.Findings = findings
			ev.Executed = append(ev.Executed, step.Tool)

		case ToolKBSearch:
			q := step.Args["query"]
			if q == "" {
				q = "suspicious transaction reporting"
			}
			// Bias retrieval toward whatever actually fired: a report about
			// structuring should cite structuring guidance, not generic CDD text.
			if len(ev.Findings) > 0 {
				var typologies []string
				for _, f := range ev.Findings {
					typologies = append(typologies, rules.Title(f.RuleID, i18n.EN))
				}
				q = strings.Join(typologies, " ") + " " + q
			}
			lang := i18n.Parse(step.Args["lang"])
			if step.Args["lang"] == "" {
				lang = plan.Lang
			}
			chunks, err := a.KB.Retrieve(ctx, q, lang, 4)
			if err != nil {
				return ev, err
			}
			ev.Chunks = chunks
			ev.Executed = append(ev.Executed, step.Tool)

		case ToolReportDraft:
			// Rendering happens in narrate(); recorded here for the audit trail.
			ev.Executed = append(ev.Executed, step.Tool)
		}
	}
	return ev, nil
}

func summarise(txns []ledger.Txn, currency string) LedgerSummary {
	s := LedgerSummary{Currency: currency, ByChannel: map[string]int{}}
	members := map[string]bool{}
	for _, t := range txns {
		s.TxnCount++
		members[t.MemberID] = true
		s.ByChannel[t.Channel]++
		if t.IsCredit() {
			s.CreditMinor += t.AmountMinor
		} else {
			s.DebitMinor += t.AmountMinor
		}
		if t.AmountMinor > s.LargestMinor {
			s.LargestMinor = t.AmountMinor
		}
	}
	s.MemberCount = len(members)
	return s
}

// ── narration ────────────────────────────────────────────────────────────────

const systemPromptEN = `You are Kanzu Agent, an offline compliance assistant for a Ugandan savings and credit cooperative (SACCO).

Absolute rules:
1. The EVIDENCE block is the only source of fact. Never introduce a member, amount, date, transaction or regulation that is not in it.
2. Findings were produced by a deterministic rule engine. Do not add findings, remove findings, or change a severity.
3. Never state or imply that a member committed a crime. Report patterns and the obligation to review.
4. If the evidence is insufficient for something you were asked, say so plainly in one sentence.
5. Cite only the sources listed under SOURCES, by their exact text.
6. Be concise. No preamble, no apology, no restating these rules.`

const systemPromptLG = `Ggwe Kanzu Agent, omulimba w'okukuuma amateeka ogufanya obufuzi oba kuggalawo okuva mu SACCO ya Uganda.

Amateeka agakakanyizibwa:
1. Ekitundu kya EVIDENCE kye kimu kyokka ky'amazima. Toongera mupolisi, omuwendo, olunaku, kkyenfuna oba teeka etali mu kimu.
2. Ebirabika byatondebwa mu mwanzi ogw'amateeka agateegeeka. Toongera, toggyawo, towangula bungi bw'ebirabika.
3. Togamba wadde okujjula nti mupolisi yakola obusaasi. Bikkula entegeka n'omukwano gw'okunoonyereza.
4. Singa obukakafu butaweeza kintu kyekigyerekwa, sse kyo mu jumlaa emu.
5. Taja ensibuko eziri wansi wa SOURCES yokka, mu bigambo byazo byamaaso.
6. Beera mufupi. Tewali okutangaaza, okusabirirwa, wadde kuddamu amateeka gano.

Okuddamu kwonna kube mu Luganda.`

const systemPromptSW = `Wewe ni Kanzu Agent, msaidizi wa uzingatiaji unaofanya kazi nje ya mtandao kwa SACCO ya Uganda.

Kanuni za lazima:
1. Sehemu ya EVIDENCE ni chanzo pekee cha ukweli. Usiongeze mwanachama, kiasi, tarehe, muamala au kanuni ambayo haipo humo.
2. Matokeo yametolewa na mfumo wa kanuni za uhakika. Usiongeze, usiondoe, na usibadilishe uzito wa matokeo.
3. Usiseme au usidokeze kwamba mwanachama ametenda uhalifu. Eleza mtindo na wajibu wa kufanya uchunguzi.
4. Kama ushahidi hautoshi, sema hivyo kwa sentensi moja.
5. Taja vyanzo vilivyoorodheshwa chini ya SOURCES pekee, kwa maneno yao halisi.
6. Kuwa mfupi. Bila utangulizi, bila kuomba radhi, bila kurudia kanuni hizi.

Jibu lote liwe kwa Kiswahili.`

// loadPromptFile reads a named prompt template from the prompt directory.
// Returns the empty string when the file cannot be read, so callers fall
// back to the embedded constant — degraded but correct.
func loadPromptFile(dir, name string) string {
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	return s
}

// systemPromptFromDir loads the narration system prompt from the versioned
// file in var/prompts/, falling back to the embedded constant when absent.
func systemPromptFromDir(dir string, lang i18n.Lang) string {
	name := "prompt-narration-en.txt"
	switch lang {
	case i18n.SW:
		name = "prompt-narration-sw.txt"
	case i18n.LG:
		name = "prompt-narration-lg.txt"
	}
	if s := loadPromptFile(dir, name); s != "" {
		return s
	}
	// Fall back to embedded constant so the agent still works when the
	// prompt directory is absent (e.g. in unit tests without the var/ tree).
	switch lang {
	case i18n.SW:
		return systemPromptSW
	case i18n.LG:
		return systemPromptLG
	default:
		return systemPromptEN
	}
}

func systemPrompt(lang i18n.Lang) string {
	switch lang {
	case i18n.SW:
		return systemPromptSW
	case i18n.LG:
		return systemPromptLG
	default:
		return systemPromptEN
	}
}

// chatML wraps a system and user message in Qwen2.5's prompt format.
//
// Built by hand because the runner invokes llama-cli with -no-cnv and a raw
// prompt file: llama.cpp applies no chat template in that mode. Getting these
// control tokens wrong is the difference between an instruction-following model
// and a text completer that rambles.
func chatML(system, user string) string {
	var b strings.Builder
	b.WriteString("<|im_start|>system\n")
	b.WriteString(system)
	b.WriteString("<|im_end|>\n<|im_start|>user\n")
	b.WriteString(user)
	b.WriteString("<|im_end|>\n<|im_start|>assistant\n")
	return b.String()
}

// renderEvidence formats Evidence for the prompt under a character budget.
//
// The budget exists because the context window is 2048 tokens and prompt
// processing is the slowest phase on CPU. Every character here costs latency, so
// the ordering is deliberate: findings first (they are the point), then the
// member, then aggregates, then citations trimmed to fit.
func renderEvidence(ev Evidence, lang i18n.Lang, budget int) string {
	var b strings.Builder
	cur := ev.Summary.Currency
	if cur == "" {
		cur = "UGX"
	}

	fmt.Fprintf(&b, "WINDOW: %s (%s to %s)\n",
		ev.Window.Label,
		ev.Window.Start.Format("2006-01-02"),
		ev.Window.End.AddDate(0, 0, -1).Format("2006-01-02"))

	if ev.Member != nil {
		m := ev.Member
		pep := "no"
		if m.IsPEP {
			pep = "yes"
		}
		fmt.Fprintf(&b, "MEMBER: %s (%s) kyc_tier=%d risk_band=%s pep=%s branch=%s\n",
			m.ID, m.Name, m.KYCLevel, m.RiskBand, pep, m.HomeBranch)
	}

	s := ev.Summary
	fmt.Fprintf(&b, "LEDGER: %d transactions, %d members, credits %s, debits %s, largest %s\n",
		s.TxnCount, s.MemberCount,
		ledger.Money(s.CreditMinor, cur), ledger.Money(s.DebitMinor, cur),
		ledger.Money(s.LargestMinor, cur))
	if len(s.ByChannel) > 0 {
		channels := make([]string, 0, len(s.ByChannel))
		for ch := range s.ByChannel {
			channels = append(channels, ch)
		}
		sort.Strings(channels)
		parts := make([]string, 0, len(channels))
		for _, ch := range channels {
			parts = append(parts, fmt.Sprintf("%s=%d", ch, s.ByChannel[ch]))
		}
		fmt.Fprintf(&b, "CHANNELS: %s\n", strings.Join(parts, " "))
	}

	if len(ev.Findings) == 0 {
		b.WriteString("FINDINGS: none. No rule triggered in this window.\n")
	} else {
		b.WriteString("FINDINGS (deterministic, authoritative):\n")
		max := len(ev.Findings)
		if max > 6 {
			max = 6
		}
		for i, f := range ev.Findings[:max] {
			fmt.Fprintf(&b, "  F%d rule=%s severity=%s member=%s(%s)\n      typology: %s\n      evidence: %s\n      transactions: %s\n",
				i+1, f.RuleID, f.Severity, f.MemberID, f.MemberName,
				rules.Title(f.RuleID, lang), f.Describe(lang), joinMax(f.TxnIDs, 8))
		}
		if len(ev.Findings) > max {
			fmt.Fprintf(&b, "  (+%d further findings not shown)\n", len(ev.Findings)-max)
		}
	}

	// Citations get whatever budget remains.
	if len(ev.Chunks) > 0 {
		remaining := budget - b.Len()
		if remaining < 400 {
			remaining = 400
		}
		perChunk := remaining / len(ev.Chunks)
		if perChunk > 700 {
			perChunk = 700
		}
		if perChunk < 160 {
			perChunk = 160
		}
		b.WriteString("SOURCES (local knowledge base):\n")
		for i, c := range ev.Chunks {
			cite := c.Citation
			if cite == "" {
				cite = c.Source
			}
			fmt.Fprintf(&b, "  S%d [%s] %s\n      %s\n", i+1, cite, c.Title, rag.Snippet(c.Body, perChunk))
		}
	}
	return b.String()
}

func joinMax(ids []string, n int) string {
	if len(ids) == 0 {
		return "none"
	}
	if len(ids) <= n {
		return strings.Join(ids, ",")
	}
	return strings.Join(ids[:n], ",") + fmt.Sprintf(",+%d more", len(ids)-n)
}

// taskInstruction states what the model must produce, per intent and language.
//
// The Kiswahili and Luganda variants end with an explicit opening anchor
// ("start your answer with the word X"). Qwen2.5-1.5B follows English
// instructions reliably, but in a lower-resource language it tends to continue
// a structured template by restating it rather than filling it in. Naming the
// first token it must emit converts an ambiguous "here is a form" into an
// unambiguous continuation and stops the echo.
func taskInstruction(intent Intent, lang i18n.Lang, request string) string {
	sw := lang == i18n.SW
	lg := lang == i18n.LG
	switch intent {
	case IntentReport:
		if lg {
			return `Wandiika PPAPULA Y'EBIKOLWA EBITEEBEREZEBWA eri akakiiko ka SACCO, mu Luganda, mu ngeri eno:

OBUFUNZE: sentensi bbiri eziraga ekyazuuliddwa.
EBIRABIKA: akatundu kamu ku buli F# — engeri, omuwendo, n'ensonga lwaki kyetaagisa okukeberwa.
OBUVUNAANYIZIBWA: sentensi emu oba bbiri okuva mu SOURCES, ng'oyogera ensibuko.
EMITENDERA: emitendera esatu egy'omukungu w'amateeka.

Ddamu emiwendo n'obubonero bw'ebyenfuna nga bwe biri mu EVIDENCE.
Tandika eky'oddamu na kigambo OBUFUNZE: era ojjuze ebitundu byonna.
Toddamu biragiro bino.`
		}
		if sw {
			return `Andika ILANI YA SHUGHULI ZA KUTILIWA SHAKA kwa kamati ya SACCO, kwa Kiswahili, kwa muundo huu:

MUHTASARI: sentensi mbili zinazoeleza kilichogunduliwa.
MATOKEO: kwa kila F#, aya moja - mtindo, kiasi, na kwa nini unahitaji uchunguzi.
WAJIBU: sentensi moja au mbili kutoka SOURCES, ikitaja chanzo.
HATUA: hatua tatu mahususi zinazofuata kwa afisa wa uzingatiaji.

Tumia namba na vitambulisho vya miamala kama vilivyo kwenye EVIDENCE.
Anza jibu lako na neno MUHTASARI: na ujaze sehemu zote.
Usirudie maagizo haya.`
		}
		return `Write a SUSPICIOUS ACTIVITY NOTE for the SACCO committee, in English, in this structure:

SUMMARY: two sentences stating what was detected.
FINDINGS: one paragraph per F# - the pattern, the value, and why it warrants review.
OBLIGATION: one or two sentences drawn from SOURCES, naming the source.
ACTIONS: three specific next steps for the compliance officer.

Reproduce figures and transaction ids exactly as they appear in EVIDENCE.
Begin your answer with the word SUMMARY: and fill in every section.
Do not repeat these instructions.`

	case IntentExplain:
		if lg {
			return "Ddamu ekibuuzo ky'omukozesa mu Luganda ng'okozesa SOURCES zokka. Akatundu kamu, n'oluvannyuma yogera ensibuko. Ekibuuzo: " + request
		}
		if sw {
			return "Jibu swali la mtumiaji kwa Kiswahili ukitumia SOURCES pekee. Aya moja, kisha taja chanzo. Swali: " + request
		}
		return "Answer the user's question in English using only SOURCES. One paragraph, then name the source. Question: " + request

	case IntentProfile:
		if lg {
			return `Wa ebifunze ku mbeera y'akabi ya memba mu Luganda:
EMBEERA: sentensi bbiri ku ddaala lya KYC n'akabi.
ENGERI: ekirabika mu bbanga lino.
EKIRAGIRO: emitendera ebiri.
Tandika na kigambo EMBEERA: Toddamu biragiro bino.`
		}
		if sw {
			return `Toa maelezo mafupi ya wasifu wa hatari wa mwanachama kwa Kiswahili:
HALI: sentensi mbili kuhusu kiwango cha utambulisho na hatari.
MFUMO: kilichoonekana katika kipindi hiki.
PENDEKEZO: hatua mbili.
Anza na neno HALI: Usirudie maagizo haya.`
		}
		return `Give a short member risk profile in English:
STATUS: two sentences on KYC tier and risk band.
PATTERN: what the window shows.
RECOMMENDATION: two actions.`

	default:
		if lg {
			return `Funza ebirabika mu Luganda: sentensi bbiri ez'obufunze, n'oluvannyuma akatundu kamu ku buli F# akalaga engeri n'omuwendo. Singa tewali birabika, kyogere mu sentensi emu. Toddamu biragiro bino.`
		}
		if sw {
			return `Fupisha matokeo kwa Kiswahili: sentensi mbili za muhtasari, kisha orodha ya risasi moja kwa kila F# ikieleza mtindo na kiasi. Kama hakuna matokeo, sema hivyo kwa sentensi moja. Usirudie maagizo haya.`
		}
		return `Summarise the findings in English: two sentences of overview, then one bullet per F# stating the pattern and the value. If there are no findings, say so in one sentence.`
	}
}

// planningSystemPrompt returns the planning system prompt, preferring the
// versioned file and falling back to a minimal embedded string.
func (a *Agent) planningSystemPrompt(lang i18n.Lang) string {
	name := "prompt-planning-en.txt"
	switch lang {
	case i18n.SW:
		name = "prompt-planning-sw.txt"
	case i18n.LG:
		name = "prompt-planning-lg.txt"
	}
	if s := loadPromptFile(a.PromptDir, name); s != "" {
		return s
	}
	return "You are a planning component. You emit JSON arrays of tool calls and nothing else."
}

func (a *Agent) narrate(ctx context.Context, request string, plan Plan, ev Evidence) (string, *llm.Result, error) {
	// Two independent caps, whichever is tighter.
	//
	// The context window is a hard limit: overflowing it makes llama.cpp drop the
	// front of the prompt, which would silently discard the system rules. Roughly
	// 3.5 characters per token for this mixed English/Kiswahili text.
	ctxBudget := (a.Cfg.ContextTokens - a.Cfg.MaxTokens - 220) * 7 / 2
	// The latency cap is a product decision: see config.PromptBudgetChars.
	budget := a.Cfg.PromptBudgetChars
	if ctxBudget < budget {
		budget = ctxBudget
	}
	if budget < 900 {
		budget = 900
	}

	task := "\nTASK:\n" + taskInstruction(plan.Intent, plan.Lang, request)
	user := renderEvidence(ev, plan.Lang, budget-len(task)) + task
	prompt := chatML(systemPromptFromDir(a.PromptDir, plan.Lang), user)

	res, err := a.LLM.Generate(ctx, llm.Request{
		Prompt:      prompt,
		MaxTokens:   a.Cfg.MaxTokens,
		Temperature: a.Cfg.Temperature,
		TopP:        a.Cfg.TopP,
		Seed:        a.Cfg.Seed,
		Label:       "narrate:" + string(plan.Intent),
	})
	if err != nil {
		return "", nil, err
	}
	return res.Text, res, nil
}

// refinePlan asks the model to improve the deterministic plan, then validates the
// result. A rejected proposal costs one inference and changes nothing.
func (a *Agent) refinePlan(ctx context.Context, request string, base Plan) (Plan, []string) {
	var tools strings.Builder
	for _, t := range Registry {
		fmt.Fprintf(&tools, "- %s(%s): %s\n", t.Name, strings.Join(t.Args, ", "), t.Desc)
	}

	user := fmt.Sprintf(`Available tools (you may use no others):
%s
Hard ordering rules: ledger.query before rules.scan; rules.scan before report.draft.

Operator request: %q
Classified intent: %s
Resolved window: %s to %s

Return ONLY a JSON array of steps, each {"tool": "...", "args": {...}, "why": "..."}. No prose.`,
		tools.String(), request, base.Intent,
		base.Window.Start.Format("2006-01-02"), base.Window.End.Format("2006-01-02"))

	prompt := chatML(a.planningSystemPrompt(base.Lang), user)

	res, err := a.LLM.Generate(ctx, llm.Request{
		Prompt:      prompt,
		MaxTokens:   220,
		Temperature: 0.1, // planning wants determinism, not variety
		TopP:        0.9,
		Seed:        a.Cfg.Seed,
		Label:       "plan:" + string(base.Intent),
	})
	if err != nil {
		return base, []string{"planner inference failed; kept deterministic plan: " + err.Error()}
	}
	return ValidatePlanJSON(res.Text, base)
}

// decodeSteps parses a JSON array of steps, tolerating the arg-value types a
// small model reaches for (numbers and booleans instead of strings).
func decodeSteps(raw string, out *[]Step) error {
	var loose []struct {
		Tool string                     `json:"tool"`
		Args map[string]json.RawMessage `json:"args"`
		Why  string                     `json:"why"`
	}
	if err := json.Unmarshal([]byte(raw), &loose); err != nil {
		return err
	}
	for _, l := range loose {
		s := Step{Tool: l.Tool, Why: l.Why, Args: map[string]string{}}
		for k, v := range l.Args {
			var str string
			if err := json.Unmarshal(v, &str); err == nil {
				s.Args[k] = str
				continue
			}
			s.Args[k] = strings.Trim(string(v), `"`)
		}
		*out = append(*out, s)
	}
	return nil
}

func (a *Agent) persistCase(ctx context.Context, plan Plan, ev Evidence, narrative string) (int64, error) {
	memberID := ev.Findings[0].MemberID
	if ev.Member != nil {
		memberID = ev.Member.ID
	}
	alertIDs := make([]int64, 0, len(ev.Findings))
	for _, f := range ev.Findings {
		alerts, err := a.DB.AlertsForMember(ctx, f.MemberID)
		if err != nil {
			return 0, err
		}
		for _, al := range alerts {
			if al.RuleID == f.RuleID && al.WindowStart.Equal(f.WindowStart) {
				alertIDs = append(alertIDs, al.ID)
				break
			}
		}
	}

	title := fmt.Sprintf("%s — %s (%s)",
		rules.Title(ev.Findings[0].RuleID, plan.Lang), memberID, plan.Window.Label)

	return a.DB.OpenCase(ctx, ledger.Case{
		MemberID:  memberID,
		Title:     title,
		Lang:      string(plan.Lang),
		Narrative: narrative,
		Citations: strings.Join(rag.Citations(ev.Chunks), "\n"),
	}, alertIDs)
}

// RenderDeterministic produces the model-free view of an outcome. This is what an
// operator sees when weights are missing, and what the demo shows first so a
// reviewer can confirm the narration adds nothing factual.
func RenderDeterministic(out *Outcome, lang i18n.Lang) string {
	var b strings.Builder
	ev := out.Evidence
	cur := ev.Summary.Currency
	if cur == "" {
		cur = "UGX"
	}

	fmt.Fprintf(&b, "%s: %s (%s → %s)\n", i18n.T(lang, "report.period"), ev.Window.Label,
		ev.Window.Start.Format("2006-01-02"), ev.Window.End.AddDate(0, 0, -1).Format("2006-01-02"))
	fmt.Fprintf(&b, "ledger: %d transactions · %d members · credits %s · debits %s\n",
		ev.Summary.TxnCount, ev.Summary.MemberCount,
		ledger.Money(ev.Summary.CreditMinor, cur), ledger.Money(ev.Summary.DebitMinor, cur))

	if len(ev.Findings) == 0 {
		b.WriteString("\n" + i18n.T(lang, "alerts.none") + "\n")
		return b.String()
	}

	fmt.Fprintf(&b, "\n%d %s\n\n", len(ev.Findings), i18n.T(lang, "alerts.count"))
	for i, f := range ev.Findings {
		fmt.Fprintf(&b, "  [%s] %s — %s (%s)\n", strings.ToUpper(f.Severity),
			rules.Title(f.RuleID, lang), f.MemberName, f.MemberID)
		fmt.Fprintf(&b, "      %s\n", f.Describe(lang))
		fmt.Fprintf(&b, "      rule=%s txns=%s\n", f.RuleID, joinMax(f.TxnIDs, 6))
		if i < len(ev.Findings)-1 {
			b.WriteByte('\n')
		}
	}

	if cites := rag.Citations(ev.Chunks); len(cites) > 0 {
		fmt.Fprintf(&b, "\n%s:\n", i18n.T(lang, "report.citations"))
		for _, c := range cites {
			fmt.Fprintf(&b, "  · %s\n", c)
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (a *Agent) setLastPlan(p Plan) {
	a.lastPlanMu.Lock()
	defer a.lastPlanMu.Unlock()
	a.lastPlan = p
}
