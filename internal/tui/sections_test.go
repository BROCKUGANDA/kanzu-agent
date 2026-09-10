package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kanzu-agent/kanzu/internal/agent"
	"github.com/kanzu-agent/kanzu/internal/i18n"
)

// stubAgent satisfies AgentFunc without touching the model or the ledger.
func stubAgent(context.Context, string, i18n.Lang) (*agent.Outcome, error) {
	return &agent.Outcome{}, nil
}

func testProviders() Providers {
	return Providers{
		Scan: func(context.Context, int) ([]AlertRow, error) {
			return []AlertRow{{
				RuleID: "R01", Title: "Structuring", Member: "Aisha Nakato",
				MemberID: "M001", Severity: "high", Detail: "5 deposits", Txns: "T1",
			}}, nil
		},
		Cases: func(context.Context) ([]CaseRow, error) {
			return []CaseRow{{
				ID: 7, Title: "SAR draft", Member: "Aisha Nakato",
				Lang: "en", Status: "draft", Opened: "2026-08-22",
			}}, nil
		},
		Members: func(context.Context) ([]MemberRow, error) {
			return []MemberRow{{
				ID: "M001", Name: "Aisha Nakato", Branch: "Kampala",
				RiskBand: "high", KYCLevel: 2, PEP: true,
			}}, nil
		},
		Inbox: func(context.Context) ([]InboxRow, error) {
			return []InboxRow{{
				ID: 1, Direction: "inbound", Peer: "Branch7",
				Body: "check txns", Status: "pending", Created: "2026-08-22 10:00",
			}}, nil
		},
		KB: func(context.Context) ([]KBRow, error) {
			return []KBRow{{Lang: "en", Chunks: 21}, {Lang: "lg", Chunks: 11}}, nil
		},
		Policy: func(context.Context) ([]PolicyRow, error) {
			return []PolicyRow{{Key: "ugx_cash_threshold", Value: "28000000"}}, nil
		},
		Doctor: func(context.Context) ([]DoctorRow, error) {
			return []DoctorRow{{Label: "weights", State: "ok", Detail: "present"}}, nil
		},
	}
}

// newSizedApp returns an App that already knows its terminal size.
func newSizedApp(t *testing.T, p Providers) *App {
	t.Helper()
	a := NewApp(stubAgent, p, true)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return a
}

// TestSectionsRenderDistinctContent is the regression guard for the bug where
// Tab moved the sidebar highlight but the body stayed on Chat.
func TestSectionsRenderDistinctContent(t *testing.T) {
	cases := []struct {
		section int
		name    string
		want    []string
	}{
		{SecScan, "Quick Scan", []string{"Quick Scan", "HIGH", "Structuring", "Aisha Nakato"}},
		{SecReports, "Reports", []string{"Reports", "SAR draft", "draft"}},
		{SecMembers, "Members", []string{"Members", "M001", "Aisha Nakato", "Kampala", "PEP"}},
		{SecInbox, "Inbox Queue", []string{"Inbox Queue", "inbound", "Branch7", "pending"}},
		{SecKB, "Knowledge Base", []string{"Knowledge Base", "EN", "21", "LG"}},
		{SecPolicy, "Policy", []string{"Policy", "ugx_cash_threshold", "28000000"}},
		{SecDoctor, "Doctor", []string{"Doctor", "weights", "ok"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newSizedApp(t, testProviders())

			cmd := a.gotoSection(tc.section)
			if cmd == nil {
				t.Fatalf("gotoSection(%d) returned no load command", tc.section)
			}
			// Run the loader synchronously and feed the result back in.
			a.Update(cmd())

			if a.navIdx != tc.section {
				t.Fatalf("navIdx = %d, want %d", a.navIdx, tc.section)
			}
			st := a.sections[tc.section]
			if st.err != nil {
				t.Fatalf("section %d load error: %v", tc.section, st.err)
			}
			if !st.loaded {
				t.Fatalf("section %d not marked loaded", tc.section)
			}

			view := a.View()
			for _, want := range tc.want {
				if !strings.Contains(view, want) {
					t.Errorf("view for %s missing %q", tc.name, want)
				}
			}
			// The chat input must not bleed into a read-only section.
			if strings.Contains(view, "Enter send") {
				t.Errorf("%s rendered the chat footer", tc.name)
			}
		})
	}
}

// TestChatIsDefaultSection guards the initial view.
func TestChatIsDefaultSection(t *testing.T) {
	a := newSizedApp(t, testProviders())
	if a.navIdx != SecChat {
		t.Fatalf("navIdx = %d, want SecChat", a.navIdx)
	}
	view := a.View()
	if !strings.Contains(view, "SACCO Compliance Chat") {
		t.Error("chat header missing from default view")
	}
	if !strings.Contains(view, "Enter send") {
		t.Error("chat footer missing from default view")
	}
}

// TestNilProviderDegradesGracefully: a half-provisioned install must not panic.
func TestNilProviderDegradesGracefully(t *testing.T) {
	a := newSizedApp(t, Providers{}) // every provider nil

	cmd := a.gotoSection(SecMembers)
	if cmd == nil {
		t.Fatal("expected a load command even with a nil provider")
	}
	a.Update(cmd())

	if a.sections[SecMembers].err == nil {
		t.Fatal("expected an error for a nil provider")
	}
	view := a.View() // must render, not panic
	if !strings.Contains(view, "no data provider") {
		t.Errorf("view did not surface the missing provider, got:\n%s", view)
	}
}

// TestTabCyclesAndWraps checks Tab / Shift+Tab navigation.
func TestTabCyclesAndWraps(t *testing.T) {
	a := newSizedApp(t, testProviders())

	for i := 1; i < secCount; i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyTab})
		if a.navIdx != i {
			t.Fatalf("after %d tabs navIdx = %d, want %d", i, a.navIdx, i)
		}
	}
	// One more Tab wraps back to Chat.
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	if a.navIdx != SecChat {
		t.Fatalf("Tab did not wrap to Chat, navIdx = %d", a.navIdx)
	}
	// Shift+Tab from Chat wraps to the last section.
	a.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if a.navIdx != secCount-1 {
		t.Fatalf("Shift+Tab did not wrap to last section, navIdx = %d", a.navIdx)
	}
}

// TestReloadSectionIgnoresChat: Chat has no snapshot to refetch.
func TestReloadSectionIgnoresChat(t *testing.T) {
	a := newSizedApp(t, testProviders())
	if cmd := a.reloadSection(SecChat); cmd != nil {
		t.Error("reloadSection(SecChat) should be a no-op")
	}
}
