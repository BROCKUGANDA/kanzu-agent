package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kanzu-agent/kanzu/internal/i18n"
)

// Intent is the coarse classification of an operator request.
type Intent string

const (
	IntentScan    Intent = "scan_suspicious"
	IntentReport  Intent = "draft_report"
	IntentExplain Intent = "explain_typology"
	IntentProfile Intent = "member_profile"
	IntentSummary Intent = "ledger_summary"
	IntentHelp    Intent = "help"
)

// Tool names. These are the only callable operations, and the LLM planner is
// validated against exactly this set: a proposed step naming anything else is
// discarded rather than attempted.
const (
	ToolLedgerQuery  = "ledger.query"
	ToolRulesScan    = "rules.scan"
	ToolKBSearch     = "kb.search"
	ToolMemberLookup = "member.profile"
	ToolReportDraft  = "report.draft"
)

// ToolSpec documents a tool for the planner prompt and for validation.
type ToolSpec struct {
	Name string
	Args []string
	Desc string
}

// Registry is the single source of truth for what the agent can do.
var Registry = []ToolSpec{
	{ToolLedgerQuery, []string{"start_date", "end_date", "member_id"},
		"Read transactions from the local SQLite ledger for a date window. Deterministic."},
	{ToolRulesScan, []string{"rule_ids"},
		"Run the deterministic AML rule engine over the queried window. Produces every alert. No model involved."},
	{ToolKBSearch, []string{"query", "lang"},
		"Retrieve passages from the local AML/SACCO knowledge base with BM25. Returns citations."},
	{ToolMemberLookup, []string{"member"},
		"Load one member's KYC tier, risk band, PEP status and alert history."},
	{ToolReportDraft, []string{"template", "lang"},
		"Render a suspicious-activity note from findings already produced by rules.scan."},
}

// KnownTool reports whether name is callable.
func KnownTool(name string) bool {
	for _, t := range Registry {
		if t.Name == name {
			return true
		}
	}
	return false
}

// Step is one planned tool call.
type Step struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
	Why  string            `json:"why,omitempty"`
}

// Plan is an ordered list of tool calls plus the classification that produced it.
type Plan struct {
	Intent Intent
	Lang   i18n.Lang
	Window Window
	Member string
	Steps  []Step
	Origin string // "deterministic" | "model-refined"
	Notes  []string
}

// Window is a closed-open time interval.
type Window struct {
	Start time.Time
	End   time.Time
	Label string
}

// Describe renders the plan for display.
func (p Plan) Describe() string {
	var b strings.Builder
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "  %d. %s(", i+1, s.Tool)
		keys := make([]string, 0, len(s.Args))
		for k := range s.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for j, k := range keys {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s=%q", k, s.Args[k])
		}
		b.WriteString(")")
		if s.Why != "" {
			fmt.Fprintf(&b, "\n       %s", s.Why)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ── intent classification ────────────────────────────────────────────────────

// Classify determines intent from lexical evidence in either language.
//
// Deliberately not a model call. Intent routing decides whether the rule engine
// runs, and a misrouted request that silently skips rules.scan is the worst
// failure mode this system has: the operator receives a fluent narrative with no
// detection behind it. Keyword routing is dull and it is auditable.
func Classify(text string) Intent {
	concepts := map[string]bool{}
	for _, c := range i18n.ConceptsIn(text) {
		concepts[c] = true
	}
	toks := map[string]bool{}
	for _, t := range i18n.Tokenize(text) {
		toks[t] = true
	}

	switch {
	case toks["help"] || toks["msaada"] || toks["amri"]:
		return IntentHelp
	// Report before scan: "flag X and draft a note" must draft, and drafting
	// implies scanning as a prerequisite step anyway.
	case concepts["draft"] || toks["note"] || toks["ripoti"] || toks["taarifa"] || toks["ilani"] || toks["report"]:
		return IntentReport
	case concepts["explain"]:
		return IntentExplain
	case concepts["scan"] || concepts["suspicious"] || concepts["structuring"]:
		return IntentScan
	case concepts["summary"]:
		return IntentSummary
	case concepts["member"]:
		return IntentProfile
	default:
		return IntentScan
	}
}

// ── window parsing ───────────────────────────────────────────────────────────

var (
	reISODate  = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	reLastNDay = regexp.MustCompile(`\b(?:last|past|siku)\s+(\d{1,3})\s*(?:days?|siku)?\b`)
	reMemberID = regexp.MustCompile(`\b([Mm]-?\d{3,5})\b`)
)

// ParseWindow extracts a date range from free text in either language.
//
// Windows are closed-open [start, end) and snapped to midnight UTC. "Last week"
// means the seven calendar days ending today, not the previous ISO week: an
// officer asking on Wednesday means the last seven days.
func ParseWindow(text string, now time.Time) Window {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)

	if m := reISODate.FindAllStringSubmatch(text, 2); len(m) > 0 {
		parse := func(g []string) time.Time {
			y, _ := strconv.Atoi(g[1])
			mo, _ := strconv.Atoi(g[2])
			d, _ := strconv.Atoi(g[3])
			return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
		}
		start := parse(m[0])
		end := tomorrow
		if len(m) > 1 {
			end = parse(m[1]).AddDate(0, 0, 1)
		}
		if !end.After(start) {
			end = start.AddDate(0, 0, 1)
		}
		return Window{Start: start, End: end,
			Label: start.Format("2006-01-02") + " to " + end.AddDate(0, 0, -1).Format("2006-01-02")}
	}

	if m := reLastNDay.FindStringSubmatch(strings.ToLower(text)); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n <= 366 {
			return Window{Start: tomorrow.AddDate(0, 0, -n), End: tomorrow,
				Label: fmt.Sprintf("last %d days", n)}
		}
	}

	lower := strings.ToLower(text)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}

	switch {
	case has("today", "leo"):
		return Window{Start: today, End: tomorrow, Label: "today"}
	case has("yesterday", "jana"):
		return Window{Start: today.AddDate(0, 0, -1), End: today, Label: "yesterday"}
	case has("month", "mwezi"):
		return Window{Start: tomorrow.AddDate(0, 0, -30), End: tomorrow, Label: "last 30 days"}
	case has("quarter", "robo"):
		return Window{Start: tomorrow.AddDate(0, 0, -90), End: tomorrow, Label: "last 90 days"}
	case has("week", "wiki"):
		return Window{Start: tomorrow.AddDate(0, 0, -7), End: tomorrow, Label: "last 7 days"}
	}

	// Default: 7 days. Stated explicitly in the plan so the operator can see the
	// assumption rather than discover it in the output.
	return Window{Start: tomorrow.AddDate(0, 0, -7), End: tomorrow, Label: "last 7 days (default)"}
}

// ParseMember extracts a member reference: an id like M-014, or the token after
// "member"/"mwanachama".
func ParseMember(text string) string {
	if m := reMemberID.FindStringSubmatch(text); m != nil {
		id := strings.ToUpper(m[1])
		if !strings.Contains(id, "-") {
			id = "M-" + strings.TrimPrefix(id, "M")
		}
		return id
	}
	fields := strings.Fields(text)
	for i, f := range fields {
		l := strings.ToLower(strings.Trim(f, ".,:;?!"))
		if l == "member" || l == "mwanachama" || l == "for" || l == "kwa" {
			if i+1 < len(fields) {
				cand := strings.Trim(fields[i+1], ".,:;?!\"'")
				if cand != "" && !isCommonWord(cand) {
					return cand
				}
			}
		}
	}
	return ""
}

func isCommonWord(s string) bool {
	switch strings.ToLower(s) {
	case "the", "a", "an", "this", "that", "all", "any", "last", "week", "month",
		"today", "profile", "wiki", "mwezi", "leo", "zote", "hii":
		return true
	}
	return false
}

// ── deterministic plan construction ──────────────────────────────────────────

// BuildPlan produces the canonical plan for a request.
//
// Ordering is a correctness property, not a style choice. rules.scan must precede
// report.draft, because a report rendered before detection completes would be a
// narrative with nothing behind it. The executor enforces the same invariant
// independently, so a model-refined plan cannot reorder its way past it.
func BuildPlan(text string, now time.Time) Plan {
	lang := i18n.DetectLang(text)
	intent := Classify(text)
	window := ParseWindow(text, now)
	member := ParseMember(text)

	p := Plan{Intent: intent, Lang: lang, Window: window, Member: member, Origin: "deterministic"}

	ledgerArgs := map[string]string{
		"start_date": window.Start.Format("2006-01-02"),
		"end_date":   window.End.Format("2006-01-02"),
	}
	if member != "" {
		ledgerArgs["member_id"] = member
	}

	switch intent {
	case IntentHelp:
		return p

	case IntentExplain:
		p.Steps = []Step{
			{ToolKBSearch, map[string]string{"query": text, "lang": string(lang)},
				"Answer from retrieved local regulation text, not from model memory."},
		}

	case IntentProfile:
		p.Steps = []Step{
			{ToolMemberLookup, map[string]string{"member": member},
				"KYC tier, risk band and PEP status gate everything downstream."},
			{ToolLedgerQuery, ledgerArgs, "Read the member's activity for the window."},
			{ToolRulesScan, map[string]string{"rule_ids": "ALL"},
				"Evaluate all eight typologies so the profile reflects current exposure."},
		}

	case IntentSummary:
		p.Steps = []Step{
			{ToolLedgerQuery, ledgerArgs, "Aggregate volumes and channels for the window."},
		}

	case IntentScan:
		p.Steps = []Step{
			{ToolLedgerQuery, ledgerArgs, "Load the window from the local ledger."},
			{ToolRulesScan, map[string]string{"rule_ids": "ALL"},
				"Deterministic detection. Every flag originates here, never from the model."},
			{ToolKBSearch, map[string]string{"query": typologyQuery(text), "lang": string(lang)},
				"Attach the regulatory basis for whatever fired."},
		}

	case IntentReport:
		p.Steps = []Step{
			{ToolLedgerQuery, ledgerArgs, "Load the window from the local ledger."},
			{ToolRulesScan, map[string]string{"rule_ids": "ALL"},
				"Findings must exist before a note can describe them."},
			{ToolKBSearch, map[string]string{"query": typologyQuery(text), "lang": string(lang)},
				"Retrieve citable obligations for the note."},
			{ToolReportDraft, map[string]string{"template": "sar_note", "lang": string(lang)},
				"Model narrates the findings. It cannot add, remove or reclassify them."},
		}
	}
	return p
}

// typologyQuery biases retrieval toward the compliance concepts in the request,
// falling back to a general query when the request is vague.
func typologyQuery(text string) string {
	concepts := i18n.ConceptsIn(text)
	if len(concepts) == 0 {
		return "suspicious transaction reporting obligations customer due diligence"
	}
	return strings.Join(concepts, " ") + " suspicious transaction reporting obligation"
}

// ── model-refined planning ───────────────────────────────────────────────────

var reJSONArray = regexp.MustCompile(`(?s)\[.*\]`)

// ValidatePlanJSON parses a model-proposed plan and keeps only what is safe.
//
// This is the guard that makes LLM planning acceptable in a compliance tool.
// Accepted: reordering, dropping an unnecessary retrieval, adjusting a query
// string. Rejected: unknown tools, unknown argument names, and any plan that
// fails the invariants (scan before draft, ledger before scan). On any rejection
// the caller keeps the deterministic plan, so a confused model degrades to
// correct behaviour rather than to no behaviour.
func ValidatePlanJSON(raw string, fallback Plan) (Plan, []string) {
	var notes []string
	match := reJSONArray.FindString(raw)
	if match == "" {
		return fallback, []string{"model produced no JSON array; kept deterministic plan"}
	}

	var proposed []Step
	if err := decodeSteps(match, &proposed); err != nil {
		return fallback, []string{"model plan was not valid JSON; kept deterministic plan"}
	}
	if len(proposed) == 0 {
		return fallback, []string{"model plan was empty; kept deterministic plan"}
	}

	allowed := map[string]map[string]bool{}
	for _, spec := range Registry {
		args := map[string]bool{}
		for _, a := range spec.Args {
			args[a] = true
		}
		allowed[spec.Name] = args
	}

	var kept []Step
	for _, s := range proposed {
		s.Tool = strings.TrimSpace(s.Tool)
		argSpec, ok := allowed[s.Tool]
		if !ok {
			notes = append(notes, fmt.Sprintf("dropped unknown tool %q", s.Tool))
			continue
		}
		clean := map[string]string{}
		for k, v := range s.Args {
			if argSpec[k] {
				clean[k] = v
			} else {
				notes = append(notes, fmt.Sprintf("dropped unknown argument %q for %s", k, s.Tool))
			}
		}
		s.Args = clean
		kept = append(kept, s)
	}

	if !invariantsHold(kept) {
		notes = append(notes, "model plan violated ordering invariants; kept deterministic plan")
		fallback.Notes = append(fallback.Notes, notes...)
		return fallback, notes
	}

	// Carry over arguments the model cannot know (resolved dates, member id).
	refined := fallback
	refined.Origin = "model-refined"
	refined.Steps = mergeArgs(kept, fallback)
	refined.Notes = append(refined.Notes, notes...)
	return refined, notes
}

// invariantsHold enforces the ordering guarantees.
func invariantsHold(steps []Step) bool {
	idx := func(tool string) int {
		for i, s := range steps {
			if s.Tool == tool {
				return i
			}
		}
		return -1
	}
	query, scan, draft := idx(ToolLedgerQuery), idx(ToolRulesScan), idx(ToolReportDraft)

	if draft >= 0 && scan < 0 {
		return false // narrating findings that were never computed
	}
	if draft >= 0 && scan > draft {
		return false // narrating before detecting
	}
	if scan >= 0 && query < 0 {
		return false // scanning without loading data
	}
	if scan >= 0 && query > scan {
		return false
	}
	return true
}

// mergeArgs fills model-proposed steps with the resolved arguments from the
// deterministic plan, so a model that writes "last_week" as a date cannot corrupt
// the actual query window.
func mergeArgs(steps []Step, base Plan) []Step {
	authoritative := map[string]map[string]string{}
	for _, s := range base.Steps {
		authoritative[s.Tool] = s.Args
	}
	out := make([]Step, 0, len(steps))
	for _, s := range steps {
		if auth, ok := authoritative[s.Tool]; ok {
			merged := map[string]string{}
			for k, v := range s.Args {
				merged[k] = v
			}
			// Dates and member ids are resolved locally and always win.
			for _, k := range []string{"start_date", "end_date", "member_id", "member", "rule_ids"} {
				if v, ok := auth[k]; ok {
					merged[k] = v
				}
			}
			s.Args = merged
		}
		out = append(out, s)
	}
	return out
}
