package tui

// Section rendering for the Kanzu Agent TUI.
//
// Architecture contract (same as app.go): nothing in this file runs SQL, calls
// the model, or evaluates AML rules. Each section renders a read-only snapshot
// that cmd/kanzu supplies through Providers. That keeps the deterministic
// rule engine and the ledger as the only places a compliance decision is made.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── section identifiers (indices into navItems in app.go) ────────────────────

const (
	SecChat = iota
	SecScan
	SecReports
	SecMembers
	SecInbox
	SecKB
	SecPolicy
	SecDoctor
	secCount
)

// errNoProvider is shown when a section has no data source wired in.
var errNoProvider = errors.New("no data provider wired for this section")

// ── read-only view models ────────────────────────────────────────────────────

// AlertRow is one deterministic rule firing, already formatted for display.
type AlertRow struct {
	RuleID   string
	Title    string
	Member   string
	MemberID string
	Severity string // "high" | "medium" | "low"
	Detail   string
	Txns     string
}

// CaseRow is a draft suspicious-activity file.
type CaseRow struct {
	ID     int64
	Title  string
	Member string
	Lang   string
	Status string
	Opened string
}

// MemberRow is one SACCO member.
type MemberRow struct {
	ID       string
	Name     string
	Branch   string
	RiskBand string
	KYCLevel int
	PEP      bool
	Dormant  bool
}

// InboxRow is one queued offline message.
type InboxRow struct {
	ID        int64
	Direction string
	Peer      string
	Lang      string
	Body      string
	Status    string
	Created   string
}

// KBRow summarises the indexed corpus for one language.
type KBRow struct {
	Lang   string
	Chunks int
}

// PolicyRow is one institutional threshold.
type PolicyRow struct {
	Key   string
	Value string
}

// DoctorRow is one health check result.
type DoctorRow struct {
	Label  string
	Detail string
	State  string // "ok" | "fail" | "warn"
}

// Providers supplies each section's data. Every field is optional: a nil
// provider renders an explicit notice rather than panicking, so the TUI still
// runs on a half-provisioned install.
type Providers struct {
	Scan    func(ctx context.Context, days int) ([]AlertRow, error)
	Cases   func(ctx context.Context) ([]CaseRow, error)
	Members func(ctx context.Context) ([]MemberRow, error)
	Inbox   func(ctx context.Context) ([]InboxRow, error)
	KB      func(ctx context.Context) ([]KBRow, error)
	Policy  func(ctx context.Context) ([]PolicyRow, error)
	Doctor  func(ctx context.Context) ([]DoctorRow, error)
}

// ── per-section state ────────────────────────────────────────────────────────

type sectionState struct {
	loaded  bool
	loading bool
	err     error

	alerts  []AlertRow
	cases   []CaseRow
	members []MemberRow
	inbox   []InboxRow
	kb      []KBRow
	policy  []PolicyRow
	doctor  []DoctorRow
}

// sectionLoadedMsg carries a completed section load back into Update.
type sectionLoadedMsg struct {
	section int
	err     error

	alerts  []AlertRow
	cases   []CaseRow
	members []MemberRow
	inbox   []InboxRow
	kb      []KBRow
	policy  []PolicyRow
	doctor  []DoctorRow
}

// loadSection returns a tea.Cmd that fetches one section's snapshot off the
// UI goroutine. Chat needs no loading — it is driven by the agent call.
func (a *App) loadSection(idx int) tea.Cmd {
	p := a.providers
	ctx := a.ctx
	days := a.scanDays

	return func() tea.Msg {
		out := sectionLoadedMsg{section: idx}
		switch idx {
		case SecScan:
			if p.Scan == nil {
				out.err = errNoProvider
				return out
			}
			out.alerts, out.err = p.Scan(ctx, days)
		case SecReports:
			if p.Cases == nil {
				out.err = errNoProvider
				return out
			}
			out.cases, out.err = p.Cases(ctx)
		case SecMembers:
			if p.Members == nil {
				out.err = errNoProvider
				return out
			}
			out.members, out.err = p.Members(ctx)
		case SecInbox:
			if p.Inbox == nil {
				out.err = errNoProvider
				return out
			}
			out.inbox, out.err = p.Inbox(ctx)
		case SecKB:
			if p.KB == nil {
				out.err = errNoProvider
				return out
			}
			out.kb, out.err = p.KB(ctx)
		case SecPolicy:
			if p.Policy == nil {
				out.err = errNoProvider
				return out
			}
			out.policy, out.err = p.Policy(ctx)
		case SecDoctor:
			if p.Doctor == nil {
				out.err = errNoProvider
				return out
			}
			out.doctor, out.err = p.Doctor(ctx)
		}
		return out
	}
}

// ── section chrome ───────────────────────────────────────────────────────────

var (
	tableHeadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Navy)).
			Bold(true)

	badgeOKStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Green)).
			Bold(true)

	badgeFailStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Red)).
			Bold(true)

	badgeWarnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Amber)).
			Bold(true)

	sectionTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(Navy)).
				Bold(true)
)

// sectionHeading is the human title + one-line purpose for each section.
func sectionHeading(idx int) (string, string) {
	switch idx {
	case SecChat:
		return "SACCO Compliance Chat", "Ask in English, Kiswahili or Luganda"
	case SecScan:
		return "Quick Scan", "Deterministic rule engine — no model involved"
	case SecReports:
		return "Reports", "Draft suspicious-activity files awaiting review"
	case SecMembers:
		return "Members", "Local member register with KYC and risk banding"
	case SecInbox:
		return "Inbox Queue", "Offline message queue (inbound and outbound)"
	case SecKB:
		return "Knowledge Base", "Indexed regulatory corpus by language"
	case SecPolicy:
		return "Policy", "Institutional thresholds every alert fires against"
	case SecDoctor:
		return "Doctor", "Model, ledger, corpus and llama.cpp wiring"
	}
	return "Kanzu Agent", ""
}

// renderSection renders the active non-chat section body.
func (a *App) renderSection(w, rows int) string {
	st := a.sections[a.navIdx]

	switch {
	case st.loading:
		return "\n" + ActiveNavStyle.Render("  ⟳  Reading local records…")
	case st.err != nil:
		return "\n" + badgeFailStyle.Render("  ⚠  ") +
			MutedStyle.Render(st.err.Error()) +
			"\n\n" + MutedStyle.Render("  press r to retry")
	case !st.loaded:
		return "\n" + MutedStyle.Render("  press r to load")
	}

	switch a.navIdx {
	case SecScan:
		return renderAlerts(st.alerts, w, rows)
	case SecReports:
		return renderCases(st.cases, w, rows)
	case SecMembers:
		return renderMembers(st.members, w, rows)
	case SecInbox:
		return renderInbox(st.inbox, w, rows)
	case SecKB:
		return renderKB(st.kb, w, rows)
	case SecPolicy:
		return renderPolicy(st.policy, w, rows)
	case SecDoctor:
		return renderDoctor(st.doctor, w, rows)
	}
	return ""
}

// ── section bodies ───────────────────────────────────────────────────────────

func renderAlerts(rows []AlertRow, w, max int) string {
	if len(rows) == 0 {
		return emptyState("No rules fired in this window.",
			"That is a clean result, not an error.")
	}
	var b strings.Builder
	b.WriteString("  " + tableHeadStyle.Render(
		cell("SEV", 8)+cell("TYPOLOGY", 34)+cell("MEMBER", 22)+"DETAIL") + "\n")
	b.WriteString("  " + SepStyle.Render(strings.Repeat("─", clampInt(w-4, 10, 200))) + "\n")

	shown, more := limit(len(rows), max-3)
	for _, r := range rows[:shown] {
		b.WriteString("  " +
			sevStyle(r.Severity).Render(cell(strings.ToUpper(r.Severity), 8)) +
			cell(r.Title, 34) +
			cell(r.Member, 22) +
			MutedStyle.Render(truncate(r.Detail, clampInt(w-70, 10, 200))) + "\n")
	}
	b.WriteString(moreLine(more))
	return b.String()
}

func renderCases(rows []CaseRow, w, max int) string {
	if len(rows) == 0 {
		return emptyState("No draft cases yet.",
			"Run a scan, then ask the agent to draft a compliance note.")
	}
	var b strings.Builder
	b.WriteString("  " + tableHeadStyle.Render(
		cell("#", 6)+cell("TITLE", 40)+cell("MEMBER", 20)+cell("LANG", 6)+cell("STATUS", 10)+"OPENED") + "\n")
	b.WriteString("  " + SepStyle.Render(strings.Repeat("─", clampInt(w-4, 10, 200))) + "\n")

	shown, more := limit(len(rows), max-3)
	for _, r := range rows[:shown] {
		b.WriteString("  " +
			cell(fmt.Sprintf("%d", r.ID), 6) +
			cell(r.Title, 40) +
			cell(r.Member, 20) +
			cell(strings.ToUpper(r.Lang), 6) +
			cell(r.Status, 10) +
			MutedStyle.Render(r.Opened) + "\n")
	}
	b.WriteString(moreLine(more))
	return b.String()
}

func renderMembers(rows []MemberRow, w, max int) string {
	if len(rows) == 0 {
		return emptyState("No members in the local ledger.", "Run: kanzu init")
	}
	var b strings.Builder
	b.WriteString("  " + tableHeadStyle.Render(
		cell("ID", 12)+cell("NAME", 26)+cell("BRANCH", 18)+cell("RISK", 10)+cell("KYC", 5)+"FLAGS") + "\n")
	b.WriteString("  " + SepStyle.Render(strings.Repeat("─", clampInt(w-4, 10, 200))) + "\n")

	shown, more := limit(len(rows), max-3)
	for _, r := range rows[:shown] {
		flags := []string{}
		if r.PEP {
			flags = append(flags, badgeWarnStyle.Render("PEP"))
		}
		if r.Dormant {
			flags = append(flags, MutedStyle.Render("dormant"))
		}
		b.WriteString("  " +
			cell(r.ID, 12) +
			cell(r.Name, 26) +
			cell(r.Branch, 18) +
			riskStyle(r.RiskBand).Render(cell(r.RiskBand, 10)) +
			cell(fmt.Sprintf("%d", r.KYCLevel), 5) +
			strings.Join(flags, " ") + "\n")
	}
	b.WriteString(moreLine(more))
	return b.String()
}

func renderInbox(rows []InboxRow, w, max int) string {
	if len(rows) == 0 {
		return emptyState("Queue is empty.",
			"Enqueue one with: kanzu send \"<message>\"")
	}
	var b strings.Builder
	b.WriteString("  " + tableHeadStyle.Render(
		cell("#", 6)+cell("DIR", 10)+cell("PEER", 18)+cell("STATUS", 12)+"BODY") + "\n")
	b.WriteString("  " + SepStyle.Render(strings.Repeat("─", clampInt(w-4, 10, 200))) + "\n")

	shown, more := limit(len(rows), max-3)
	for _, r := range rows[:shown] {
		b.WriteString("  " +
			cell(fmt.Sprintf("%d", r.ID), 6) +
			cell(r.Direction, 10) +
			cell(r.Peer, 18) +
			cell(r.Status, 12) +
			MutedStyle.Render(truncate(oneLine(r.Body), clampInt(w-50, 10, 200))) + "\n")
	}
	b.WriteString(moreLine(more))
	return b.String()
}

func renderKB(rows []KBRow, w, max int) string {
	if len(rows) == 0 {
		return emptyState("Corpus not indexed.", "Run: kanzu init")
	}
	var b strings.Builder
	b.WriteString("  " + tableHeadStyle.Render(cell("LANG", 10)+"CHUNKS") + "\n")
	b.WriteString("  " + SepStyle.Render(strings.Repeat("─", clampInt(w-4, 10, 200))) + "\n")

	total := 0
	shown, more := limit(len(rows), max-4)
	for _, r := range rows[:shown] {
		total += r.Chunks
		b.WriteString("  " +
			ActiveNavStyle.Render(cell(strings.ToUpper(r.Lang), 10)) +
			fmt.Sprintf("%d", r.Chunks) + "\n")
	}
	b.WriteString(moreLine(more))
	b.WriteString("\n  " + MutedStyle.Render(
		fmt.Sprintf("%d chunks indexed · retrieval is cross-language (EN ↔ SW ↔ LG)", total)) + "\n")
	return b.String()
}

func renderPolicy(rows []PolicyRow, w, max int) string {
	if len(rows) == 0 {
		return emptyState("No policy rows.", "Run: kanzu init")
	}
	var b strings.Builder
	b.WriteString("  " + tableHeadStyle.Render(cell("KEY", 40)+"VALUE") + "\n")
	b.WriteString("  " + SepStyle.Render(strings.Repeat("─", clampInt(w-4, 10, 200))) + "\n")

	shown, more := limit(len(rows), max-4)
	for _, r := range rows[:shown] {
		b.WriteString("  " + cell(r.Key, 40) + ActiveNavStyle.Render(r.Value) + "\n")
	}
	b.WriteString(moreLine(more))
	b.WriteString("\n  " + MutedStyle.Render("change with: kanzu policy <key> <value>") + "\n")
	return b.String()
}

func renderDoctor(rows []DoctorRow, w, max int) string {
	if len(rows) == 0 {
		return emptyState("No checks reported.", "")
	}
	var b strings.Builder
	shown, more := limit(len(rows), max-1)
	for _, r := range rows[:shown] {
		var badge string
		switch r.State {
		case "ok":
			badge = badgeOKStyle.Render("[ok  ]")
		case "warn":
			badge = badgeWarnStyle.Render("[warn]")
		default:
			badge = badgeFailStyle.Render("[FAIL]")
		}
		b.WriteString("  " + badge + " " + cell(r.Label, 20) +
			MutedStyle.Render(truncate(r.Detail, clampInt(w-32, 10, 200))) + "\n")
	}
	b.WriteString(moreLine(more))
	return b.String()
}

// ── small helpers ────────────────────────────────────────────────────────────

func emptyState(title, hint string) string {
	s := "\n  " + sectionTitleStyle.Render(title)
	if hint != "" {
		s += "\n  " + MutedStyle.Render(hint)
	}
	return s + "\n"
}

func moreLine(more int) string {
	if more <= 0 {
		return ""
	}
	return "  " + MutedStyle.Render(fmt.Sprintf("… +%d more", more)) + "\n"
}

// limit returns how many rows to show and how many are hidden.
func limit(n, room int) (int, int) {
	if room < 1 {
		room = 1
	}
	if n <= room {
		return n, 0
	}
	return room, n - room
}

// cell pads or truncates s to exactly n display columns.
func cell(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > n-1 {
		if n == 1 {
			return "…"
		}
		return string(r[:n-2]) + "… "
	}
	return s + strings.Repeat(" ", n-len(r))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if n <= 0 {
		return ""
	}
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sevStyle(sev string) lipgloss.Style {
	switch strings.ToUpper(sev) {
	case "HIGH":
		return badgeFailStyle
	case "MED", "MEDIUM":
		return badgeWarnStyle
	case "LOW":
		return badgeOKStyle
	}
	return MutedStyle
}

func riskStyle(band string) lipgloss.Style {
	switch strings.ToLower(band) {
	case "high":
		return badgeFailStyle
	case "medium":
		return badgeWarnStyle
	case "low":
		return badgeOKStyle
	}
	return MutedStyle
}
