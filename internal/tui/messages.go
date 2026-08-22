package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Role constants.
const (
	RoleUser  = "user"
	RoleAgent = "agent"
)

// ChatMessage is one entry in the conversation thread.
type ChatMessage struct {
	Role     string    // RoleUser | RoleAgent
	Text     string    // plain-text body (may contain newlines)
	Lang     string    // "EN", "SW", "LG"
	Ts       time.Time
	Severity string // "HIGH", "MED", "LOW", "" — colours the left border
}

// Render returns the lipgloss-styled string for one message, fitted to width.
func (m ChatMessage) Render(width int) string {
	ts := m.Ts.Format("15:04")

	if m.Role == RoleUser {
		meta := MutedStyle.Render("You  " + ts)
		bubble := UserBubbleStyle.Width(width - 10).Render(m.Text)
		return meta + "\n" + bubble
	}

	// Agent bubble
	langTag := ""
	if m.Lang != "" && m.Lang != "EN" {
		langTag = "  " + ActiveNavStyle.Render("["+m.Lang+"]")
	}
	label := AgentLabelStyle.Render("Kanzu Agent") + langTag + "  " + MutedStyle.Render(ts)

	body := m.Text

	// Apply finding-severity left border if set.
	var findingStyle lipgloss.Style
	switch strings.ToUpper(m.Severity) {
	case "HIGH":
		findingStyle = HighStyle
	case "MED", "MEDIUM":
		findingStyle = MediumStyle
	case "LOW":
		findingStyle = LowStyle
	default:
		findingStyle = AgentBubbleStyle.Width(width - 6)
	}
	if m.Severity != "" {
		body = findingStyle.Width(width - 8).Render(body)
	} else {
		body = AgentBubbleStyle.Width(width - 6).Render(body)
	}
	return label + "\n" + body
}
