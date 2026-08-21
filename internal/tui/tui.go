// Package tui provides Kanzu Agent's two operator surfaces: an interactive
// terminal chat, and a store-and-forward message queue for field officers.
//
// Neither is a web application, and that is a requirement rather than a
// preference. The deployment target is a branch machine with intermittent power
// and connectivity; a browser plus a local HTTP server would add a socket, a
// second runtime and a class of failure that a terminal does not have. The queue
// surface exists because the realistic field workflow is asynchronous: an officer
// with a feature phone or a laptop that is offline for hours submits a request,
// the agent processes a batch when the machine is free, and the reply waits for a
// link. Kanzu never performs delivery itself — an external sync tool drains the
// outbound table — which is exactly what keeps the agent offline by construction.
package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kanzu-agent/kanzu/internal/agent"
	"github.com/kanzu-agent/kanzu/internal/i18n"
	"github.com/kanzu-agent/kanzu/internal/ledger"
	"github.com/kanzu-agent/kanzu/internal/thermal"
)

const rule = "────────────────────────────────────────────────────────────────────"

// Session renders agent interactions to a terminal.
type Session struct {
	Agent    *agent.Agent
	Out      io.Writer
	In       io.Reader
	ShowPlan bool
	ShowPerf bool
}

// New builds a Session.
func New(a *agent.Agent, in io.Reader, out io.Writer) *Session {
	return &Session{Agent: a, In: in, Out: out, ShowPlan: true, ShowPerf: true}
}

func (s *Session) printf(format string, args ...any) {
	fmt.Fprintf(s.Out, format, args...)
}

// Ask runs one request and renders the outcome.
func (s *Session) Ask(ctx context.Context, request string) error {
	lang := s.Agent.Lang
	s.printf("\n%s\n", i18n.T(lang, "chat.thinking"))

	start := time.Now()
	out, err := s.Agent.Execute(ctx, request)
	if err != nil {
		return err
	}
	s.render(out, time.Since(start))
	return nil
}

func (s *Session) render(out *agent.Outcome, elapsed time.Duration) {
	lang := out.Plan.Lang

	if s.ShowPlan {
		s.printf("\n%s\n%s  ·  intent=%s  lang=%s  origin=%s\n%s\n",
			rule, i18n.T(lang, "plan.header"), out.Plan.Intent, lang, out.Plan.Origin, rule)
		s.printf("%s", out.Plan.Describe())
		for _, n := range out.Plan.Notes {
			s.printf("  ! %s\n", n)
		}
	}

	// Deterministic evidence is printed before any narration, always. A reviewer
	// can then confirm for themselves that the prose below adds no new facts.
	s.printf("\n%s\n%s\n%s\n", rule, i18n.T(lang, "evidence.header"), rule)
	s.printf("%s", agent.RenderDeterministic(out, lang))

	if out.Answer != "" {
		s.printf("\n%s\n%s\n%s\n\n%s\n", rule, i18n.T(lang, "report.header"), rule, out.Answer)
		s.printf("\n%s\n", wrap(i18n.T(lang, "report.disclaimer"), 76))
	} else if out.Degraded {
		s.printf("\n[%s: %s]\n", i18n.T(lang, "model.offline"), out.DegradeWhy)
	}

	if out.CaseID > 0 {
		s.printf("\ncase #%d opened in the local ledger\n", out.CaseID)
	}

	if s.ShowPerf {
		s.printf("\n%s\n", rule)
		if out.Perf != nil {
			s.printf("inference : %.1f tok/s generation · %d prompt tok · %d generated · load %.0f ms\n",
				out.Perf.GenerationTPS, out.Perf.PromptTokens, out.Perf.GeneratedTokens, out.Perf.LoadMS)
		}
		st := s.Agent.Gov.Snapshot()
		s.printf("thermal   : %s\n", formatThermal(st))
		s.printf("wall      : %s\n", elapsed.Round(100*time.Millisecond))
	}
}

func formatThermal(st thermal.Stats) string {
	temp := "no sensor"
	if st.SensorPresent {
		temp = fmt.Sprintf("peak %.1f°C", st.PeakTempC)
	}
	return fmt.Sprintf("%s · %d bursts · %d gated pauses · duty %.0f%% · cooldown %s",
		temp, st.Bursts, st.Pauses, st.DutyAchieved*100, st.TotalCooldown.Round(time.Millisecond*100))
}

// Chat runs the interactive loop.
func (s *Session) Chat(ctx context.Context) error {
	lang := s.Agent.Lang
	s.printf("%s\n", rule)
	s.printf("Kanzu Agent — %s\n", i18n.T(lang, "app.tagline"))
	s.printf("%s\n%s\n\n", i18n.T(lang, "chat.banner"), rule)

	scanner := bufio.NewScanner(s.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for {
		s.printf("\n%s> ", i18n.T(s.Agent.Lang, "chat.prompt"))
		if !scanner.Scan() {
			s.printf("\n")
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, ":") {
			stop, err := s.command(line)
			if err != nil {
				s.printf("error: %v\n", err)
				continue
			}
			if stop {
				return nil
			}
			continue
		}

		if err := s.Ask(ctx, line); err != nil {
			// An operator session must survive a bad request; only a cancelled
			// context ends the loop.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.printf("\nerror: %v\n", err)
		}
	}
}

// command handles the ":" verbs. Returns true when the session should end.
func (s *Session) command(line string) (bool, error) {
	fields := strings.Fields(line)
	switch fields[0] {
	case ":quit", ":q", ":exit":
		return true, nil

	case ":help", ":h":
		s.printf("%s\n", i18n.T(s.Agent.Lang, "chat.help"))
		return false, nil

	case ":lang":
		if len(fields) < 2 {
			s.printf("current language: %s\n", s.Agent.Lang)
			return false, nil
		}
		s.Agent.Lang = i18n.Parse(fields[1])
		s.printf("language: %s (%s)\n", s.Agent.Lang, s.Agent.Lang.Name())
		return false, nil

	case ":plan":
		p := s.Agent.LastPlan()
		if len(p.Steps) == 0 {
			s.printf("no plan yet\n")
			return false, nil
		}
		s.printf("%s (%s)\n%s", i18n.T(s.Agent.Lang, "plan.header"), p.Origin, p.Describe())
		return false, nil

	case ":stats":
		s.printf("%s\n", formatThermal(s.Agent.Gov.Snapshot()))
		return false, nil

	case ":perf":
		s.ShowPerf = !s.ShowPerf
		s.printf("perf display: %v\n", s.ShowPerf)
		return false, nil

	case ":steps":
		s.ShowPlan = !s.ShowPlan
		s.printf("plan display: %v\n", s.ShowPlan)
		return false, nil

	default:
		return false, fmt.Errorf("unknown command %q — try :help", fields[0])
	}
}

// ── offline message queue ────────────────────────────────────────────────────

// Enqueue records an inbound request from a field officer.
func Enqueue(ctx context.Context, db *ledger.DB, peer, body, lang, channel string) (int64, error) {
	if strings.TrimSpace(body) == "" {
		return 0, fmt.Errorf("empty message body")
	}
	if lang == "" {
		lang = string(i18n.DetectLang(body))
	}
	return db.Enqueue(ctx, ledger.Message{
		Direction: "inbound",
		Channel:   channel,
		Peer:      peer,
		Lang:      lang,
		Body:      body,
	})
}

// ProcessQueue drains up to limit inbound messages, replying into the outbound
// queue.
//
// Batch work is the realistic path to a sustained thermal event, so the governor
// gets an explicit inter-item pause here on top of its per-burst cooldown. An
// offline queue has no latency requirement to protect, which makes this the
// cheapest place in the system to spend time cooling.
func ProcessQueue(ctx context.Context, a *agent.Agent, db *ledger.DB, out io.Writer, limit int) error {
	msgs, err := db.PendingInbound(ctx, limit)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		fmt.Fprintf(out, "%s\n", i18n.T(a.Lang, "inbox.empty"))
		return nil
	}

	fmt.Fprintf(out, "%s\nprocessing %d queued request(s)\n%s\n", rule, len(msgs), rule)

	for i, m := range msgs {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Gov.BatchPause(ctx, i)

		lang := i18n.Parse(m.Lang)
		a.Lang = lang
		fmt.Fprintf(out, "\n[#%d from %s · %s]\n  %s\n", m.ID, orAnon(m.Peer), lang, m.Body)

		outcome, err := a.Execute(ctx, m.Body)
		if err != nil {
			if e := db.MarkProcessed(ctx, m.ID, "failed"); e != nil {
				return e
			}
			fmt.Fprintf(out, "  ! failed: %v\n", err)
			continue
		}

		reply := outcome.Answer
		if strings.TrimSpace(reply) == "" {
			// Model unavailable or narration failed: the deterministic findings
			// are still a complete, useful answer, so send those.
			reply = agent.RenderDeterministic(outcome, lang)
		}

		replyID, err := db.Enqueue(ctx, ledger.Message{
			Direction: "outbound",
			Channel:   m.Channel,
			Peer:      m.Peer,
			Lang:      string(lang),
			Body:      reply,
			ReplyTo:   m.ID,
		})
		if err != nil {
			return err
		}
		if err := db.MarkProcessed(ctx, m.ID, "processed"); err != nil {
			return err
		}

		fmt.Fprintf(out, "  → reply #%d %s\n", replyID, i18n.T(lang, "inbox.queued"))
		fmt.Fprintf(out, "  %d finding(s)\n", len(outcome.Evidence.Findings))
	}

	st := a.Gov.Snapshot()
	fmt.Fprintf(out, "\n%s\nbatch complete · %s\n", rule, formatThermal(st))
	return nil
}

// ShowOutbound lists replies waiting for a link.
func ShowOutbound(ctx context.Context, db *ledger.DB, out io.Writer, limit int) error {
	msgs, err := db.OutboundQueue(ctx, limit)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		fmt.Fprintf(out, "outbound queue is empty\n")
		return nil
	}
	fmt.Fprintf(out, "%d reply/replies awaiting delivery (Kanzu never sends these itself)\n\n", len(msgs))
	for _, m := range msgs {
		fmt.Fprintf(out, "%s\n#%d → %s · %s · in reply to #%d · %s\n%s\n%s\n",
			rule, m.ID, orAnon(m.Peer), m.Lang, m.ReplyTo,
			m.CreatedAt.Format("2006-01-02 15:04"), rule, indent(m.Body, "  "))
	}
	return nil
}

func orAnon(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unnamed officer)"
	}
	return s
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// wrap breaks text at word boundaries, for the fixed-width disclaimer block.
func wrap(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	lineLen := 0
	for i, w := range words {
		if lineLen > 0 && lineLen+1+len(w) > width {
			b.WriteByte('\n')
			lineLen = 0
		} else if i > 0 {
			b.WriteByte(' ')
			lineLen++
		}
		b.WriteString(w)
		lineLen += len(w)
	}
	return b.String()
}
