package tui

import "github.com/charmbracelet/lipgloss"

// Brand palette — matches the Kanzu Agent logo.
const (
	Navy  = "#0D1B3E"
	Green = "#1A6B3A"
	Gold  = "#C9A227"
	Cyan  = "#00B4D8"
	Ink   = "#152238"
	Mist  = "#EAF1F7"
	White = "#FFFFFF"
	Red   = "#B42318"
	Amber = "#B54708"
	Slate = "#667085"
	Line  = "#D0D5DD"
)

var (
	AppStyle = lipgloss.NewStyle().
			Background(lipgloss.Color(White)).
			Foreground(lipgloss.Color(Ink))

	SidebarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color(Navy)).
			Foreground(lipgloss.Color(White)).
			Padding(1, 2)

	BrandStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Gold)).
			Bold(true)

	ActiveNavStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Cyan)).
			Bold(true)

	MutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Slate))

	UserBubbleStyle = lipgloss.NewStyle().
			Background(lipgloss.Color(Navy)).
			Foreground(lipgloss.Color(White)).
			Padding(0, 1).
			MarginLeft(8).
			MarginTop(1)

	AgentBubbleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(Mist)).
				Foreground(lipgloss.Color(Ink)).
				Padding(1, 2).
				MarginRight(4).
				MarginTop(1)

	AgentLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Navy)).
			Bold(true)

	OfflineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Green)).
			Bold(true)

	InputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(Cyan)).
				Padding(0, 1)

	HighStyle = lipgloss.NewStyle().
			BorderLeft(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color(Red)).
			PaddingLeft(1)

	MediumStyle = lipgloss.NewStyle().
			BorderLeft(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color(Amber)).
			PaddingLeft(1)

	LowStyle = lipgloss.NewStyle().
			BorderLeft(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color(Green)).
			PaddingLeft(1)

	SepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Line))

	HintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Cyan)).
			Italic(true)
)
