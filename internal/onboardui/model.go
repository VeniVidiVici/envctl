package onboardui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/onboard"
)

type MachineWriter interface {
	Write(
		configRoot string,
		machine envconfig.Machine,
		replaceExisting bool,
	) (string, error)
}

type FileWriter struct{}

func (FileWriter) Write(
	configRoot string,
	machine envconfig.Machine,
	replaceExisting bool,
) (string, error) {
	return onboard.WriteMachine(configRoot, machine, replaceExisting)
}

type machineWrittenMsg struct {
	path string
	err  error
}

type Model struct {
	result             onboard.Result
	configRoot         string
	writer             MachineWriter
	profileCursor      int
	confirming         bool
	written            bool
	continueAfterWrite bool
	editingMachineID   bool
	machineIDDraft     string
	machineIDFresh     bool
	width              int
	statusMessage      string
}

func New(
	result onboard.Result,
	configRoot string,
	writer MachineWriter,
) *Model {
	model := &Model{
		result: result, configRoot: configRoot, writer: writer, width: 90,
	}
	if result.Status == onboard.StatusUnmatched && result.Proposal != nil {
		model.editingMachineID = true
		model.machineIDDraft = result.Proposal.ID
		model.machineIDFresh = true
	}
	return model
}

func (m *Model) ContinueIntoSetup() *Model {
	m.continueAfterWrite = true
	return m
}

func (m *Model) WrittenMachineID() string {
	if !m.written || m.result.Proposal == nil {
		return ""
	}
	return m.result.Proposal.ID
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
		if message.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.editingMachineID {
			return m.updateMachineID(message)
		}
		switch message.String() {
		case "q":
			return m, tea.Quit
		case "down", "j":
			m.moveProfile(1)
		case "up", "k":
			m.moveProfile(-1)
		case "space":
			m.toggleProfile()
		case "e":
			m.beginMachineIDEdit()
		case "w":
			m.requestWrite()
		case "n", "esc":
			if m.confirming {
				m.confirming = false
				m.statusMessage = "Write cancelled"
			}
		case "y":
			if m.confirming {
				m.confirming = false
				return m, m.writeMachine()
			}
		}
	case machineWrittenMsg:
		if message.err != nil {
			m.statusMessage = "Could not write machine config: " + message.err.Error()
			return m, nil
		}
		m.written = true
		m.statusMessage = "Wrote " + message.path + ".\n" +
			"Review the Git diff, then rerun onboard to preview this machine."
		if m.continueAfterWrite {
			m.statusMessage = "Registered " + m.result.Proposal.ID +
				"; continuing into guided setup."
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Model) View() tea.View {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#88C0D0"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7B88A1"))
	highlightStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ECEFF4")).
		Background(lipgloss.Color("#3B4252"))
	warningStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#EBCB8B"))
	successStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#A3BE8C"))

	var body strings.Builder
	body.WriteString(titleStyle.Render("envctl onboard"))
	body.WriteString(mutedStyle.Render("  identify and register this Mac"))
	body.WriteString("\n\n")
	body.WriteString(fmt.Sprintf(
		"Mac: %s  %s  %s\n",
		m.result.Identity.Model,
		m.result.Identity.Chip,
		m.result.Identity.OSVersion,
	))
	body.WriteString(fmt.Sprintf(
		"Host: %s    Local host: %s\n",
		m.result.Identity.Hostname,
		m.result.Identity.LocalHostname,
	))
	fingerprint := m.result.Identity.HardwareUUIDSHA256
	if len(fingerprint) > 16 {
		fingerprint = fingerprint[:16] + "…"
	}
	body.WriteString("Hardware identity: sha256:" + fingerprint + "\n")
	body.WriteString("Status: ")
	switch m.result.Status {
	case onboard.StatusMatched:
		body.WriteString(successStyle.Render("matched " + m.result.MachineID))
	case onboard.StatusNeedsConfirmation:
		body.WriteString(warningStyle.Render(
			"hostname matches " + m.result.MachineID +
				"; hardware identity needs confirmation",
		))
	default:
		body.WriteString(warningStyle.Render("new machine"))
	}
	body.WriteString("\n\n")

	if m.result.Status == onboard.StatusMatched {
		body.WriteString("This Mac is already registered.\n")
		if m.result.Plan == nil {
			body.WriteString("No local desired-state plan was collected.\n")
		} else {
			summary := m.result.Plan.Summary
			body.WriteString(fmt.Sprintf(
				"Packages: %d satisfied   %d missing   %d drifted   %d extra\n",
				summary.Satisfied,
				summary.Missing,
				summary.Drifted+summary.Ambiguous,
				summary.Extra,
			))
			body.WriteString(fmt.Sprintf(
				"Proposed package actions: %d\n",
				summary.Actions,
			))
			if m.result.Plan.LinkSummary != nil {
				links := m.result.Plan.LinkSummary
				body.WriteString(fmt.Sprintf(
					"Portable links: %d satisfied   %d missing   %d drifted\n",
					links.Satisfied,
					links.Missing,
					links.Drifted+links.NotChecked,
				))
			}
			if m.result.RecoveryPlan != nil {
				recovery := m.result.RecoveryPlan.Summary
				body.WriteString(fmt.Sprintf(
					"Credential recovery: %d satisfied   %d missing   %d drifted   %d blocked\n",
					recovery.Satisfied,
					recovery.Missing,
					recovery.Drifted,
					recovery.Blocked+recovery.ToolMissing+
						recovery.SourceMissing+recovery.SourceUnsafe,
				))
			}
			if len(m.result.Plan.Warnings) > 0 {
				body.WriteString(fmt.Sprintf(
					"Plan notes: %d\n",
					len(m.result.Plan.Warnings),
				))
			}
			body.WriteString("\n")
			body.WriteString(mutedStyle.Render(
				"Preview only: onboarding has not applied these actions.",
			))
			body.WriteString("\n")
			body.WriteString("\nContinue with guided setup:\n")
			body.WriteString(fmt.Sprintf(
				"  envctl setup --config %s \\\n",
				m.configRoot,
			))
			body.WriteString(fmt.Sprintf(
				"    --machine %s --local\n",
				m.result.MachineID,
			))
		}
	} else if m.result.Proposal != nil {
		body.WriteString(fmt.Sprintf(
			"Proposed file: %s\nMachine ID: ",
			m.result.ProposalPath,
		))
		if m.editingMachineID {
			draft := m.machineIDDraft
			if draft == "" {
				draft = " "
			}
			body.WriteString(highlightStyle.Render(draft + " "))
			body.WriteString(mutedStyle.Render("  enter to accept"))
		} else {
			body.WriteString(m.result.Proposal.ID)
			if m.result.Status == onboard.StatusUnmatched {
				body.WriteString(mutedStyle.Render("  e to edit"))
			}
		}
		body.WriteString("\nAccess from fleet controller: ")
		body.WriteString(m.result.Proposal.Access.Type)
		if m.result.Proposal.Access.Host != "" {
			body.WriteString(" (" + m.result.Proposal.Access.Host + ")")
		}
		body.WriteString("\n\nProfiles:\n")
		for index, profile := range m.result.AvailableProfiles {
			selected := contains(m.result.Proposal.Profiles, profile)
			marker := "[ ]"
			if selected {
				marker = "[x]"
			}
			line := fmt.Sprintf("  %s %s", marker, profile)
			if index == m.profileCursor &&
				m.result.Status == onboard.StatusUnmatched {
				line = highlightStyle.Render(line)
			}
			body.WriteString(line + "\n")
		}
		if m.result.Status == onboard.StatusNeedsConfirmation {
			body.WriteString(mutedStyle.Render(
				"\nExisting profiles and access are preserved.\n" +
					"Only the hardware identity is added.",
			))
			body.WriteString("\n")
		}
		body.WriteString("\n")
		body.WriteString(warningStyle.Render(
			"Config only: no commit, push, package install, or link changes.",
		))
		body.WriteString("\n")
	}

	if m.confirming {
		body.WriteString("\n")
		body.WriteString(warningStyle.Render(
			"Write the proposed machine file?  y confirm   n cancel",
		))
		body.WriteString("\n")
	}
	if m.statusMessage != "" {
		body.WriteString("\n")
		if m.written {
			body.WriteString(successStyle.Render(m.statusMessage))
		} else {
			body.WriteString(m.statusMessage)
		}
		body.WriteString("\n")
	}
	body.WriteString("\n")
	if m.editingMachineID {
		body.WriteString(mutedStyle.Render(
			"type machine ID   enter accept   esc keep suggestion   ctrl+c quit",
		))
	} else if m.result.Status == onboard.StatusUnmatched {
		body.WriteString(mutedStyle.Render(
			"e edit ID   j/k profile   space toggle   w write   q quit",
		))
	} else if m.result.Status == onboard.StatusNeedsConfirmation {
		body.WriteString(mutedStyle.Render("w write   q quit"))
	} else {
		body.WriteString(mutedStyle.Render("q quit"))
	}

	view := tea.NewView(body.String())
	view.AltScreen = true
	view.WindowTitle = "envctl onboard"
	return view
}

func (m *Model) beginMachineIDEdit() {
	if m.result.Status != onboard.StatusUnmatched || m.result.Proposal == nil {
		return
	}
	m.editingMachineID = true
	m.machineIDDraft = m.result.Proposal.ID
	m.machineIDFresh = true
	m.statusMessage = ""
}

func (m *Model) updateMachineID(
	message tea.KeyPressMsg,
) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "enter":
		candidate := strings.TrimSpace(m.machineIDDraft)
		if !onboard.SafeIdentifier(candidate) {
			m.statusMessage = "Use letters, numbers, dots, underscores, or hyphens."
			return m, nil
		}
		if contains(m.result.ConfiguredMachines, candidate) &&
			candidate != m.result.Proposal.ID {
			m.statusMessage = "Machine ID already exists: " + candidate
			return m, nil
		}
		previous := m.result.Proposal.ID
		m.result.Proposal.ID = candidate
		m.result.ProposalPath = "machines/" + candidate + ".yaml"
		if m.result.Proposal.Access.Type == "ssh" &&
			(m.result.Proposal.Access.Host == "" ||
				m.result.Proposal.Access.Host == previous) {
			m.result.Proposal.Access.Host = candidate
		}
		m.machineIDDraft = candidate
		m.machineIDFresh = false
		m.editingMachineID = false
		m.statusMessage = ""
	case "esc":
		m.machineIDDraft = m.result.Proposal.ID
		m.machineIDFresh = false
		m.editingMachineID = false
		m.statusMessage = ""
	case "backspace", "ctrl+h":
		if m.machineIDFresh {
			m.machineIDDraft = ""
			m.machineIDFresh = false
			break
		}
		runes := []rune(m.machineIDDraft)
		if len(runes) > 0 {
			m.machineIDDraft = string(runes[:len(runes)-1])
		}
	default:
		if message.Text == "" {
			return m, nil
		}
		if m.machineIDFresh {
			m.machineIDDraft = ""
			m.machineIDFresh = false
		}
		m.machineIDDraft += message.Text
	}
	return m, nil
}

func (m *Model) moveProfile(delta int) {
	if m.result.Status != onboard.StatusUnmatched ||
		len(m.result.AvailableProfiles) == 0 {
		return
	}
	m.profileCursor = (m.profileCursor + delta +
		len(m.result.AvailableProfiles)) % len(m.result.AvailableProfiles)
	m.statusMessage = ""
}

func (m *Model) toggleProfile() {
	if m.result.Status != onboard.StatusUnmatched ||
		m.result.Proposal == nil ||
		len(m.result.AvailableProfiles) == 0 {
		return
	}
	profile := m.result.AvailableProfiles[m.profileCursor]
	var selected []string
	for _, existing := range m.result.Proposal.Profiles {
		if existing != profile {
			selected = append(selected, existing)
		}
	}
	if len(selected) == len(m.result.Proposal.Profiles) {
		selected = append(selected, profile)
	}
	m.result.Proposal.Profiles = selected
	m.statusMessage = ""
}

func (m *Model) requestWrite() {
	if m.result.Status == onboard.StatusMatched || m.result.Proposal == nil {
		m.statusMessage = "This Mac is already registered; nothing to write"
		return
	}
	if len(m.result.Proposal.Profiles) == 0 {
		m.statusMessage = "Select at least one profile before writing"
		return
	}
	m.confirming = true
	m.statusMessage = ""
}

func (m *Model) writeMachine() tea.Cmd {
	proposal := *m.result.Proposal
	replaceExisting := m.result.Status == onboard.StatusNeedsConfirmation
	return func() tea.Msg {
		path, err := m.writer.Write(
			m.configRoot, proposal, replaceExisting,
		)
		return machineWrittenMsg{path: path, err: err}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
