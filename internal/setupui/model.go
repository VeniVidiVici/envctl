package setupui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type PhaseID string

const (
	PhaseRecovery PhaseID = "recovery"
	PhaseLinks    PhaseID = "links"
	PhaseHomebrew PhaseID = "homebrew"
	PhaseMise     PhaseID = "mise"
	PhaseBun      PhaseID = "bun"
	PhaseMAS      PhaseID = "mas"
	PhaseManual   PhaseID = "manual"
)

type PhaseStatus string

const (
	StatusSatisfied PhaseStatus = "satisfied"
	StatusReady     PhaseStatus = "ready"
	StatusReview    PhaseStatus = "review"
	StatusBlocked   PhaseStatus = "blocked"
	StatusRunning   PhaseStatus = "running"
	StatusCompleted PhaseStatus = "completed"
	StatusReviewed  PhaseStatus = "reviewed"
	StatusFailed    PhaseStatus = "failed"
)

type Phase struct {
	ID           PhaseID     `json:"id"`
	Label        string      `json:"label"`
	Description  string      `json:"description"`
	Status       PhaseStatus `json:"status"`
	Actions      int         `json:"actions"`
	Blockers     int         `json:"blockers"`
	Dependencies []PhaseID   `json:"dependencies,omitempty"`
	Command      []string    `json:"command,omitempty"`
}

type CommandFactory interface {
	Command(Phase) (*exec.Cmd, error)
}

type ProcessFactory struct {
	Context    context.Context
	Executable string
}

func (f ProcessFactory) Command(phase Phase) (*exec.Cmd, error) {
	if f.Executable == "" {
		return nil, errors.New("envctl executable is unavailable")
	}
	if len(phase.Command) == 0 {
		return nil, errors.New("setup phase has no executable command")
	}
	ctx := f.Context
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, f.Executable, phase.Command...)
	command.Stdout = io.Discard
	command.Stderr = os.Stderr
	return command, nil
}

type phaseFinishedMsg struct {
	id  PhaseID
	err error
}

type Model struct {
	machineID     string
	phases        []Phase
	factory       CommandFactory
	cursor        int
	confirming    bool
	running       bool
	width         int
	statusMessage string
}

func New(machineID string, phases []Phase, factory CommandFactory) *Model {
	return &Model{
		machineID: machineID,
		phases:    append([]Phase(nil), phases...),
		factory:   factory,
		width:     96,
	}
}

func Run(model *Model) error {
	_, err := tea.NewProgram(model).Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	return nil
}

func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
	case tea.KeyPressMsg:
		if m.running {
			return m, nil
		}
		switch message.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "down", "j":
			m.move(1)
		case "up", "k":
			m.move(-1)
		case "enter", "x":
			return m, m.requestRun()
		case "n", "esc":
			if m.confirming {
				m.confirming = false
				m.statusMessage = "Phase cancelled"
			}
		case "y":
			if m.confirming {
				m.confirming = false
				return m, m.runSelected()
			}
		}
	case phaseFinishedMsg:
		m.running = false
		index := m.phaseIndex(message.id)
		if index < 0 {
			m.statusMessage = "Completed an unknown setup phase"
			return m, nil
		}
		if message.err != nil {
			m.phases[index].Status = StatusFailed
			m.statusMessage = fmt.Sprintf(
				"%s failed: %v",
				m.phases[index].Label,
				message.err,
			)
			return m, nil
		}
		if m.phases[index].Status == StatusReview {
			m.phases[index].Status = StatusReviewed
			m.statusMessage = m.phases[index].Label + " review completed"
		} else {
			m.phases[index].Status = StatusCompleted
			m.phases[index].Actions = 0
			m.statusMessage = m.phases[index].Label + " completed and verified"
		}
	}
	return m, nil
}

func (m *Model) View() tea.View {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#88C0D0"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7B88A1"))
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ECEFF4")).
		Background(lipgloss.Color("#3B4252"))
	warningStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#EBCB8B"))
	successStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#A3BE8C"))

	var body strings.Builder
	body.WriteString(titleStyle.Render("envctl setup"))
	body.WriteString(mutedStyle.Render("  guided first-run convergence"))
	body.WriteString("\n\n")
	body.WriteString("Machine: " + m.machineID + "\n")
	body.WriteString(mutedStyle.Render(
		"Each phase replans in a separate envctl process before it changes state.",
	))
	body.WriteString("\n\n")

	for index, phase := range m.phases {
		symbol := phaseSymbol(phase.Status)
		line := fmt.Sprintf(
			"  %s  %-20s  %-10s  %3d action(s)  %2d blocker(s)",
			symbol,
			phase.Label,
			phase.Status,
			phase.Actions,
			phase.Blockers,
		)
		if index == m.cursor {
			line = selectedStyle.Width(max(1, m.width-2)).Render(line)
		} else if phaseComplete(phase.Status) {
			line = successStyle.Render(line)
		}
		body.WriteString(line)
		body.WriteString("\n")
	}

	if len(m.phases) > 0 {
		selected := m.phases[m.cursor]
		body.WriteString("\n")
		body.WriteString(selected.Description)
		body.WriteString("\n")
		if len(selected.Dependencies) > 0 {
			body.WriteString(mutedStyle.Render(
				"Requires: " + joinPhaseIDs(selected.Dependencies),
			))
			body.WriteString("\n")
		}
		if len(selected.Command) > 0 {
			body.WriteString(mutedStyle.Render(
				"Command: envctl " + strings.Join(selected.Command, " "),
			))
			body.WriteString("\n")
		}
	}

	if m.confirming {
		body.WriteString("\n")
		body.WriteString(warningStyle.Render(
			"Run this phase now?  y confirm   n cancel",
		))
		body.WriteString("\n")
	}
	if m.running {
		body.WriteString("\n")
		body.WriteString(warningStyle.Render("Phase is running…"))
		body.WriteString("\n")
	}
	if m.statusMessage != "" {
		body.WriteString("\n")
		body.WriteString(m.statusMessage)
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(mutedStyle.Render(
		"j/k move   enter run/review   q quit",
	))

	view := tea.NewView(body.String())
	view.AltScreen = true
	view.WindowTitle = "envctl setup"
	return view
}

func (m *Model) requestRun() tea.Cmd {
	if len(m.phases) == 0 {
		m.statusMessage = "No setup phases are available"
		return nil
	}
	phase := m.phases[m.cursor]
	if !m.dependenciesComplete(phase) {
		m.statusMessage = "Complete earlier required phases first"
		return nil
	}
	switch phase.Status {
	case StatusSatisfied, StatusCompleted, StatusReviewed:
		m.statusMessage = phase.Label + " already needs no action"
		return nil
	case StatusBlocked:
		m.statusMessage = phase.Label + " is blocked; review its diagnostics"
		return nil
	case StatusReady:
		m.confirming = true
		m.statusMessage = ""
		return nil
	case StatusReview:
		return m.runSelected()
	case StatusFailed:
		m.confirming = true
		m.statusMessage = "Retry the failed phase?"
		return nil
	case StatusRunning:
		return nil
	default:
		m.statusMessage = "Unsupported setup phase state"
		return nil
	}
}

func (m *Model) runSelected() tea.Cmd {
	if len(m.phases) == 0 || m.factory == nil {
		m.statusMessage = "Setup execution is unavailable"
		return nil
	}
	phase := m.phases[m.cursor]
	command, err := m.factory.Command(phase)
	if err != nil {
		m.statusMessage = err.Error()
		return nil
	}
	m.running = true
	m.statusMessage = ""
	id := phase.ID
	return tea.ExecProcess(command, func(err error) tea.Msg {
		return phaseFinishedMsg{id: id, err: err}
	})
}

func (m *Model) dependenciesComplete(phase Phase) bool {
	for _, dependency := range phase.Dependencies {
		index := m.phaseIndex(dependency)
		if index < 0 || !phaseComplete(m.phases[index].Status) {
			return false
		}
	}
	return true
}

func (m *Model) phaseIndex(id PhaseID) int {
	for index := range m.phases {
		if m.phases[index].ID == id {
			return index
		}
	}
	return -1
}

func (m *Model) move(delta int) {
	if len(m.phases) == 0 || m.confirming {
		return
	}
	m.cursor = (m.cursor + delta + len(m.phases)) % len(m.phases)
	m.statusMessage = ""
}

func phaseComplete(status PhaseStatus) bool {
	switch status {
	case StatusSatisfied, StatusCompleted, StatusReviewed:
		return true
	default:
		return false
	}
}

func phaseSymbol(status PhaseStatus) string {
	switch status {
	case StatusSatisfied, StatusCompleted, StatusReviewed:
		return "✓"
	case StatusBlocked, StatusFailed:
		return "!"
	case StatusRunning:
		return "…"
	case StatusReview:
		return "?"
	default:
		return "○"
	}
}

func joinPhaseIDs(values []PhaseID) string {
	labels := make([]string, 0, len(values))
	for _, value := range values {
		labels = append(labels, string(value))
	}
	return strings.Join(labels, ", ")
}
