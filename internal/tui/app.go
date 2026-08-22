// Package tui provides the Bubble Tea terminal interface for Kanzu Agent.
//
// Architecture contract: the TUI only collects input, invokes the agent via
// RunAgent(), and renders the returned Outcome. No SQL, no model calls, no
// AML rules live here — those all remain in their own packages.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kanzu-agent/kanzu/internal/agent"
	"github.com/kanzu-agent/kanzu/internal/i18n"
	"github.com/kanzu-agent/kanzu/internal/rules"
)

// ── agent response message (async) ───────────────────────────────────────────

type agentResultMsg struct {
	msgs []ChatMessage
	err  error
}

// AgentFunc is a function the TUI calls to run a request against the real agent.
// Implemented in cmd/kanzu and injected at construction time to keep tui free
// of direct imports of agent internals.
type AgentFunc func(ctx context.Context, request string, lang i18n.Lang) (*agent.Outcome, error)

// ── nav items ────────────────────────────────────────────────────────────────

var navItems = []string{
	"💬  Chat",
	"⚡  Quick Scan",
	"📋  Reports",
	"👥  Members",
	"📥  Inbox Queue",
	"🔍  Knowledge Base",
	"⚙️   Policy",
	"🩺  Doctor",
}

var languages = []i18n.Lang{i18n.EN, i18n.SW, i18n.LG}

// ── model ────────────────────────────────────────────────────────────────────

// App is the Bubble Tea model for the Kanzu chat interface.
type App struct {
	// injected
	runAgent  AgentFunc
	providers Providers
	ctx       context.Context
	cancel    context.CancelFunc

	// layout
	width  int
	height int

	// state
	navIdx   int
	langIdx  int
	messages []ChatMessage
	viewport viewport.Model
	input    textarea.Model
	busy     bool
	err      error

	// non-chat sections
	sections []sectionState
	scanDays int
}

// NewApp constructs the App. Pass the real runAgent function from cmd/kanzu,
// plus the read-only section providers (any of which may be nil).
func NewApp(fn AgentFunc, p Providers) *App {
	ctx, cancel := context.WithCancel(context.Background())

	ta := textarea.New()
	ta.Placeholder = "Ask about transactions, compliance, or type :help…"
	ta.Focus()
	ta.CharLimit = 1000
	ta.SetWidth(60)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Prompt = "  "
	ta.KeyMap.InsertNewline.SetEnabled(true)

	vp := viewport.New(80, 20)

	return &App{
		runAgent:  fn,
		providers: p,
		ctx:       ctx,
		cancel:    cancel,
		input:     ta,
		viewport:  vp,
		sections:  make([]sectionState, secCount),
		scanDays:  7,
		messages: []ChatMessage{
			{
				Role: RoleAgent,
				Text: "Welcome. I'm your offline compliance assistant for Ugandan SACCOs.\n\n" +
					"Try: \"flag suspicious transactions this week\"\n" +
					"     \"chunguza miamala ya kutiliwa shaka wiki hii\"  (:lang sw)\n" +
					"     \"kebera ebyenfuna eby'obucwezi sabbiiti eno\"  (:lang lg)\n\n" +
					"Type :help for all commands. No network connection used.",
				Lang: "EN",
				Ts:   time.Now(),
			},
		},
	}
}

// ── tea.Model interface ───────────────────────────────────────────────────────

func (a *App) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, tea.EnterAltScreen)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		inputW := m.Width - 34
		if inputW < 30 {
			inputW = 30
		}
		a.input.SetWidth(inputW)
		a.viewport.Width = m.Width - 34
		a.viewport.Height = m.Height - 12
		if a.viewport.Height < 4 {
			a.viewport.Height = 4
		}
		a.refreshViewport()
		return a, nil

	case agentResultMsg:
		a.busy = false
		if m.err != nil {
			a.messages = append(a.messages, ChatMessage{
				Role: RoleAgent,
				Text: "⚠  " + m.err.Error(),
				Lang: string(a.currentLang()),
				Ts:   time.Now(),
			})
		} else {
			a.messages = append(a.messages, m.msgs...)
		}
		a.refreshViewport()
		a.viewport.GotoBottom()
		return a, nil

	case sectionLoadedMsg:
		if m.section >= 0 && m.section < len(a.sections) {
			a.sections[m.section] = sectionState{
				loaded:  true,
				loading: false,
				err:     m.err,
				alerts:  m.alerts,
				cases:   m.cases,
				members: m.members,
				inbox:   m.inbox,
				kb:      m.kb,
				policy:  m.policy,
				doctor:  m.doctor,
			}
		}
		return a, nil

	case tea.KeyMsg:
		switch m.String() {
		case "ctrl+c":
			a.cancel()
			return a, tea.Quit

		case "esc":
			// Esc in a textarea unfocuses; second Esc quits.
			if !a.input.Focused() {
				a.cancel()
				return a, tea.Quit
			}
			a.input.Blur()
			return a, nil

		case "tab":
			return a, a.gotoSection((a.navIdx + 1) % len(navItems))

		case "shift+tab":
			return a, a.gotoSection((a.navIdx - 1 + len(navItems)) % len(navItems))

		case "1", "2", "3", "4", "5", "6", "7", "8":
			// Direct section jump — only when the chat input is not capturing.
			if a.navIdx != SecChat {
				return a, a.gotoSection(int(m.String()[0] - '1'))
			}

		case "r", "R":
			// Refresh the active section (chat has nothing to refresh).
			if a.navIdx != SecChat {
				return a, a.reloadSection(a.navIdx)
			}

		case "ctrl+r":
			if a.navIdx != SecChat {
				return a, a.reloadSection(a.navIdx)
			}

		case "ctrl+l":
			a.langIdx = (a.langIdx + 1) % len(languages)
			a.input.Placeholder = placeholderFor(a.currentLang())
			return a, nil

		case "enter":
			if a.navIdx != SecChat {
				return a, nil
			}
			if a.busy {
				return a, nil
			}
			raw := strings.TrimSpace(a.input.Value())
			if raw == "" {
				return a, nil
			}
			a.input.Reset()

			// Handle colon commands locally.
			if strings.HasPrefix(raw, ":") {
				return a.handleCommand(raw)
			}

			// Regular request → user bubble + async agent call.
			a.messages = append(a.messages, ChatMessage{
				Role: RoleUser,
				Text: raw,
				Lang: string(a.currentLang()),
				Ts:   time.Now(),
			})
			// Thinking indicator
			a.messages = append(a.messages, ChatMessage{
				Role: RoleAgent,
				Text: thinkingText(a.currentLang()),
				Lang: string(a.currentLang()),
				Ts:   time.Now(),
			})
			a.busy = true
			a.refreshViewport()
			a.viewport.GotoBottom()
			return a, a.callAgent(raw)
		}
	}

	// Only the chat section owns the textarea; other sections are read-only
	// views, so keys must not be swallowed by the input.
	if a.navIdx != SecChat {
		a.viewport, _ = a.viewport.Update(msg)
		return a, nil
	}

	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)

	// Also forward to viewport when input not focused.
	if !a.input.Focused() {
		a.viewport, _ = a.viewport.Update(msg)
	}
	return a, cmd
}

// gotoSection switches the active section, loading its snapshot on first visit.
func (a *App) gotoSection(idx int) tea.Cmd {
	a.navIdx = idx
	if idx == SecChat {
		a.input.Focus()
		a.refreshViewport()
		return textarea.Blink
	}
	a.input.Blur()
	if a.sections[idx].loaded || a.sections[idx].loading {
		return nil
	}
	return a.reloadSection(idx)
}

// reloadSection forces a refetch of one section.
func (a *App) reloadSection(idx int) tea.Cmd {
	if idx <= SecChat || idx >= len(a.sections) {
		return nil
	}
	a.sections[idx] = sectionState{loading: true}
	return a.loadSection(idx)
}

// ── view ──────────────────────────────────────────────────────────────────────

func (a *App) View() string {
	if a.width == 0 {
		return "Starting Kanzu Agent…"
	}
	sidebar := a.renderSidebar()
	content := a.renderContent()
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)
}

func (a *App) renderSidebar() string {
	var b strings.Builder

	// Brand
	b.WriteString(BrandStyle.Render("✦ KANZU") + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(Gold)).
		Render("  AGENT") + "\n")
	b.WriteString(MutedStyle.Render("  Offline compliance\n  copilot for SACCOs") + "\n\n")

	b.WriteString(SepStyle.Render(strings.Repeat("─", 24)) + "\n\n")

	// Nav
	for i, item := range navItems {
		if i == a.navIdx {
			b.WriteString(ActiveNavStyle.Render("▌ " + item) + "\n")
		} else {
			b.WriteString(MutedStyle.Render("  " + item) + "\n")
		}
	}

	b.WriteString("\n" + SepStyle.Render(strings.Repeat("─", 24)) + "\n\n")

	// Status
	b.WriteString(OfflineStyle.Render("● OFFLINE") + "\n")
	b.WriteString(MutedStyle.Render("  SQLite · llama.cpp") + "\n")
	b.WriteString(MutedStyle.Render("  Qwen 1.5B Q4_K_M") + "\n\n")

	// Language
	b.WriteString(MutedStyle.Render("  Lang: ") + ActiveNavStyle.Render(string(a.currentLang())) + "\n")
	b.WriteString(MutedStyle.Render("  Ctrl+L to cycle") + "\n")
	b.WriteString(MutedStyle.Render("  EN → SW → LG → EN") + "\n\n")

	// Help
	b.WriteString(MutedStyle.Render("  Tab   next section") + "\n")
	b.WriteString(MutedStyle.Render("  1-8   jump section") + "\n")
	b.WriteString(MutedStyle.Render("  r     refresh view") + "\n")
	b.WriteString(MutedStyle.Render("  Esc   quit") + "\n")

	return SidebarStyle.Width(28).Height(a.height).Render(b.String())
}

func (a *App) renderContent() string {
	w := a.width - 32
	if w < 40 {
		w = 40
	}

	// ── header ──
	title, subtitle := sectionHeading(a.navIdx)
	header := lipgloss.NewStyle().
		Width(w).
		BorderBottom(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(Line)).
		Padding(0, 1).
		Render(
			lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(Navy)).
				Render(title) +
				"  " + MutedStyle.Render(subtitle) +
				"  " + OfflineStyle.Render("● Offline"),
		)

	// ── non-chat sections: read-only tables ──
	if a.navIdx != SecChat {
		rows := a.height - 8
		if rows < 4 {
			rows = 4
		}
		body := a.renderSection(w, rows)
		footer := MutedStyle.Render(
			"  Tab/Shift+Tab section  •  1-8 jump  •  r refresh  •  Esc quit")
		return lipgloss.NewStyle().
			Width(w).
			Padding(0, 1).
			Render(lipgloss.JoinVertical(lipgloss.Left,
				header,
				"",
				body,
				"",
				footer,
			))
	}

	// ── thread (viewport) ──
	a.viewport.Width = w
	a.viewport.Height = a.height - 11
	if a.viewport.Height < 4 {
		a.viewport.Height = 4
	}
	thread := a.viewport.View()

	// ── busy strip ──
	busyLine := ""
	if a.busy {
		busyLine = ActiveNavStyle.Render("  ⟳  Kanzu is reviewing local records…") + "\n"
	}

	// ── hint bar ──
	hints := MutedStyle.Render("  Try: \"flag suspicious txns this week\"  •  \":lang sw\"  •  \":scan\"  •  \":help\"")

	// ── input ──
	inp := InputBorderStyle.Width(w - 4).Render(a.input.View())

	// ── footer ──
	footer := MutedStyle.Render("  Enter send  •  Shift+Enter newline  •  Ctrl+L lang  •  Esc quit")

	return lipgloss.NewStyle().
		Width(w).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Left,
			header,
			"",
			thread,
			"",
			busyLine+hints,
			"",
			inp,
			footer,
		))
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (a *App) currentLang() i18n.Lang {
	return languages[a.langIdx]
}

func (a *App) refreshViewport() {
	w := a.viewport.Width
	if w < 20 {
		w = 60
	}
	var b strings.Builder
	for _, m := range a.messages {
		b.WriteString(m.Render(w))
		b.WriteString("\n")
	}
	a.viewport.SetContent(b.String())
}

func (a *App) handleCommand(raw string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(raw)
	switch fields[0] {
	case ":quit", ":q":
		a.cancel()
		return a, tea.Quit

	case ":lang":
		if len(fields) >= 2 {
			nl := i18n.Parse(fields[1])
			for idx, l := range languages {
				if l == nl {
					a.langIdx = idx
					break
				}
			}
		}
		a.messages = append(a.messages, ChatMessage{
			Role: RoleAgent,
			Text: "Language set to " + a.currentLang().Name() + " (" + string(a.currentLang()) + ")",
			Lang: string(a.currentLang()),
			Ts:   time.Now(),
		})

	case ":help":
		a.messages = append(a.messages, ChatMessage{
			Role: RoleAgent,
			Text: helpText(),
			Ts:   time.Now(),
		})

	case ":scan":
		a.messages = append(a.messages, ChatMessage{
			Role: RoleUser, Text: raw, Ts: time.Now(),
		})
		a.messages = append(a.messages, ChatMessage{
			Role: RoleAgent, Text: thinkingText(a.currentLang()), Ts: time.Now(),
		})
		a.busy = true
		a.refreshViewport()
		a.viewport.GotoBottom()
		return a, a.callAgent("flag suspicious transactions this week")

	default:
		a.messages = append(a.messages, ChatMessage{
			Role: RoleAgent,
			Text: fmt.Sprintf("Unknown command %q. Type :help for options.", fields[0]),
			Ts:   time.Now(),
		})
	}

	a.refreshViewport()
	a.viewport.GotoBottom()
	return a, nil
}

// callAgent dispatches the request to the real agent in a goroutine and
// returns a tea.Cmd that delivers the result back into the Update loop.
func (a *App) callAgent(request string) tea.Cmd {
	lang := a.currentLang()
	fn := a.runAgent
	ctx := a.ctx
	return func() tea.Msg {
		// Remove the placeholder thinking bubble before appending real result.
		outcome, err := fn(ctx, request, lang)
		if err != nil {
			return agentResultMsg{err: err}
		}
		return agentResultMsg{msgs: outcomeToMessages(outcome, lang)}
	}
}

// outcomeToMessages converts an agent.Outcome into one or more ChatMessages.
// Only renders; makes zero decisions about what is suspicious.
func outcomeToMessages(out *agent.Outcome, lang i18n.Lang) []ChatMessage {
	var msgs []ChatMessage
	ts := time.Now()

	// ── Execution plan ──
	if len(out.Plan.Steps) > 0 {
		var pb strings.Builder
		pb.WriteString("Execution plan  (intent=" + string(out.Plan.Intent) + "  origin=" + out.Plan.Origin + ")\n")
		for i, s := range out.Plan.Steps {
			args := make([]string, 0, len(s.Args))
			for k, v := range s.Args {
				args = append(args, k+"="+v)
			}
			pb.WriteString(fmt.Sprintf("  %d. %s(%s)\n", i+1, s.Tool, strings.Join(args, ", ")))
		}
		msgs = append(msgs, ChatMessage{
			Role: RoleAgent, Lang: string(lang), Text: pb.String(), Ts: ts,
		})
	}

	ev := out.Evidence

	// ── Ledger summary ──
	if ev.Summary.TxnCount > 0 {
		cur := ev.Summary.Currency
		if cur == "" {
			cur = "UGX"
		}
		summaryText := fmt.Sprintf(
			"Window: %s  (%s → %s)\n"+
				"Ledger: %d transactions · %d members · credits %s · debits %s",
			ev.Window.Label,
			ev.Window.Start.Format("2006-01-02"),
			ev.Window.End.AddDate(0, 0, -1).Format("2006-01-02"),
			ev.Summary.TxnCount, ev.Summary.MemberCount,
			fmtMinor(ev.Summary.CreditMinor, cur),
			fmtMinor(ev.Summary.DebitMinor, cur),
		)
		msgs = append(msgs, ChatMessage{
			Role: RoleAgent, Lang: string(lang), Text: summaryText, Ts: ts,
		})
	}

	// ── Findings ── one bubble per finding with severity border
	if len(ev.Findings) == 0 {
		msgs = append(msgs, ChatMessage{
			Role: RoleAgent,
			Lang: string(lang),
			Text: i18n.T(lang, "alerts.none"),
			Ts:   ts,
		})
	} else {
		for _, f := range ev.Findings {
			findingText := fmt.Sprintf(
				"[%s]  %s\n%s (%s)\n%s\nrule=%s  txns=%s",
				strings.ToUpper(f.Severity),
				rules.Title(f.RuleID, lang),
				f.MemberName, f.MemberID,
				f.Describe(lang),
				f.RuleID,
				joinIDs(f.TxnIDs, 6),
			)
			msgs = append(msgs, ChatMessage{
				Role:     RoleAgent,
				Lang:     string(lang),
				Text:     findingText,
				Severity: f.Severity,
				Ts:       ts,
			})
		}
	}

	// ── Narration (model output) ──
	if out.Answer != "" {
		msgs = append(msgs, ChatMessage{
			Role: RoleAgent,
			Lang: string(lang),
			Text: "─── Compliance Note ───────────────────────────\n" + out.Answer,
			Ts:   ts,
		})
	} else if out.Degraded {
		msgs = append(msgs, ChatMessage{
			Role: RoleAgent,
			Lang: string(lang),
			Text: "[" + i18n.T(lang, "model.offline") + ": " + out.DegradeWhy + "]",
			Ts:   ts,
		})
	}

	// ── Sources ──
	if len(ev.Chunks) > 0 {
		var sb strings.Builder
		sb.WriteString("Sources (local knowledge base):\n")
		seen := map[string]bool{}
		for _, c := range ev.Chunks {
			cit := strings.TrimSpace(c.Citation)
			if cit == "" {
				cit = c.Source
			}
			if cit == "" || seen[cit] {
				continue
			}
			seen[cit] = true
			sb.WriteString("  · " + cit + "\n")
		}
		msgs = append(msgs, ChatMessage{
			Role: RoleAgent, Lang: string(lang), Text: sb.String(), Ts: ts,
		})
	}

	// ── Perf ──
	if out.Perf != nil {
		perfText := fmt.Sprintf("%.1f tok/s generation · %d prompt tok · %d generated · load %.0f ms",
			out.Perf.GenerationTPS, out.Perf.PromptTokens, out.Perf.GeneratedTokens, out.Perf.LoadMS)
		msgs = append(msgs, ChatMessage{
			Role: RoleAgent, Lang: string(lang), Text: perfText, Ts: ts,
		})
	}

	return msgs
}

// ── static helpers ────────────────────────────────────────────────────────────

func placeholderFor(l i18n.Lang) string {
	switch l {
	case i18n.SW:
		return "Andika ombi lako hapa… (:lang en kurudi Kiingereza)"
	case i18n.LG:
		return "Wandiika omubazi gwo wano… (:lang en okudda mu Lungereza)"
	default:
		return "Ask about transactions, compliance, or type :help…"
	}
}

func thinkingText(l i18n.Lang) string {
	return i18n.T(l, "chat.thinking")
}

func helpText() string {
	return `Commands:
  :lang en|sw|lg   switch language
  :scan            run rule engine (last 7 days, deterministic)
  :plan            show last execution plan
  :stats           show thermal stats
  :quit / :q       exit

  Enter            send message
  Shift+Enter      new line in message
  Ctrl+L           cycle language EN → SW → LG → EN
  Tab              move sidebar selection
  Esc              unfocus input / quit`
}

func joinIDs(ids []string, n int) string {
	if len(ids) == 0 {
		return "none"
	}
	if len(ids) <= n {
		return strings.Join(ids, ",")
	}
	return strings.Join(ids[:n], ",") + fmt.Sprintf(",+%d", len(ids)-n)
}

func fmtMinor(minor int64, cur string) string {
	// Simple formatting — matches ledger.Money output style.
	whole := minor / 100
	frac := minor % 100
	if frac < 0 {
		frac = -frac
	}
	if cur == "UGX" {
		return fmt.Sprintf("UGX %s", fmtThousands(whole))
	}
	return fmt.Sprintf("%s %d.%02d", cur, whole, frac)
}

func fmtThousands(n int64) string {
	if n < 0 {
		return "-" + fmtThousands(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}
