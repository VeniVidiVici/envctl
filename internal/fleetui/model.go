package fleetui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/VeniVidiVici/envctl/internal/fleetreconcile"
	"github.com/VeniVidiVici/envctl/internal/model"
)

type Machine struct {
	ID               string
	Profiles         []string
	Plan             model.Plan
	CollectedAt      time.Time
	RefreshStatus    string
	RefreshError     string
	RetainedLastGood bool
}

type Decision struct {
	MachineID    string
	InventoryKey string
	Value        string
}

type DecisionWriter interface {
	SaveDecision(machineID, inventoryKey, value, profile string) error
}

type Reconciler interface {
	Preview(machineID string) (fleetreconcile.Plan, error)
	Command(machineID string) (*exec.Cmd, error)
}

type decisionSavedMsg struct {
	machineID    string
	inventoryKey string
	value        string
	err          error
}

type reconcilePreviewMsg struct {
	plan fleetreconcile.Plan
	err  error
}

type reconcileFinishedMsg struct {
	err error
}

type Model struct {
	machines      []Machine
	writer        DecisionWriter
	reconciler    Reconciler
	launchProcess func(*exec.Cmd, tea.ExecCallback) tea.Cmd
	decisions     map[string]string
	machineIndex  int
	cursor        int
	filterIndex   int
	width         int
	height        int
	statusMessage string
	preview       *fleetreconcile.Plan
	previewing    bool
	confirming    bool
	running       bool
}

var filters = []string{"attention", "missing", "extra", "not-checked", "all"}

func New(
	machines []Machine,
	decisions []Decision,
	writer DecisionWriter,
) *Model {
	decisionMap := make(map[string]string)
	for _, decision := range decisions {
		decisionMap[decisionMapKey(decision.MachineID, decision.InventoryKey)] = decision.Value
	}
	return &Model{
		machines:      machines,
		writer:        writer,
		decisions:     decisionMap,
		launchProcess: tea.ExecProcess,
		width:         100,
		height:        30,
	}
}

func (m *Model) WithReconciler(reconciler Reconciler) *Model {
	m.reconciler = reconciler
	return m
}

func (m *Model) SelectMachine(machineID string) *Model {
	for index, machine := range m.machines {
		if machine.ID == machineID {
			m.machineIndex = index
			break
		}
	}
	return m
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
		m.height = message.Height
		m.clampCursor()
	case tea.KeyPressMsg:
		if m.running || m.previewing {
			return m, nil
		}
		if m.confirming {
			switch message.String() {
			case "y":
				m.confirming = false
				return m, m.runReconciliation()
			case "n", "esc":
				m.preview = nil
				m.confirming = false
				m.statusMessage = "Reconciliation cancelled"
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		switch message.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "right", "l", "]", "tab":
			m.moveMachine(1)
		case "left", "h", "[", "shift+tab":
			m.moveMachine(-1)
		case "down", "j":
			m.moveCursor(1)
		case "up", "k":
			m.moveCursor(-1)
		case "f":
			m.filterIndex = (m.filterIndex + 1) % len(filters)
			m.cursor = 0
			m.statusMessage = ""
		case "e":
			m.filterIndex = filterIndex("extra")
			m.cursor = 0
			m.statusMessage = "Showing installed packages that are absent from desired state"
		case "a":
			return m, m.saveSelectedDecision("adopt")
		case "i":
			return m, m.saveSelectedDecision("ignore")
		case "p":
			return m, m.saveSelectedDecision("keep")
		case "x":
			return m, m.saveSelectedDecision("remove")
		case "c":
			return m, m.saveSelectedDecision("clear")
		case "r":
			return m, m.previewReconciliation()
		case "n", "esc":
			if m.preview != nil {
				m.preview = nil
				m.statusMessage = "Reconciliation preview dismissed"
			}
		}
	case decisionSavedMsg:
		if message.err != nil {
			m.statusMessage = "Could not save decision: " + message.err.Error()
			return m, nil
		}
		key := decisionMapKey(message.machineID, message.inventoryKey)
		if message.value == "clear" {
			delete(m.decisions, key)
			m.statusMessage = "Decision cleared"
		} else {
			m.decisions[key] = message.value
			m.statusMessage = "Decision saved: " + message.value
		}
	case reconcilePreviewMsg:
		m.previewing = false
		if message.err != nil {
			m.statusMessage = "Could not preview reconciliation: " + message.err.Error()
			return m, nil
		}
		m.preview = &message.plan
		planned := plannedActions(message.plan.Actions)
		if !message.plan.Ready {
			m.statusMessage = fmt.Sprintf(
				"Reconciliation has %d blocker(s); review the preview",
				len(message.plan.Blockers),
			)
			return m, nil
		}
		if len(planned) == 0 {
			m.statusMessage = "No reviewed changes are ready to reconcile"
			return m, nil
		}
		m.confirming = true
	case reconcileFinishedMsg:
		m.running = false
		if message.err != nil {
			m.statusMessage = "Reconciliation failed: " + message.err.Error()
			return m, nil
		}
		m.applySuccessfulPreview()
		m.statusMessage = "Reconciliation completed and verified"
	}
	return m, nil
}

func (m *Model) View() tea.View {
	if len(m.machines) == 0 {
		view := tea.NewView("envctl fleet\n\nNo machines were loaded.\n")
		view.AltScreen = true
		view.WindowTitle = "envctl fleet"
		return view
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#88C0D0"))
	activeTabStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#2E3440")).
		Background(lipgloss.Color("#88C0D0")).
		Padding(0, 1)
	tabStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#D8DEE9")).
		Padding(0, 1)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7B88A1"))
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ECEFF4")).
		Background(lipgloss.Color("#3B4252"))
	detailStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#4C566A")).
		Padding(0, 1)

	var body strings.Builder
	body.WriteString(titleStyle.Render("envctl fleet"))
	body.WriteString(mutedStyle.Render(
		"  review extras here, then apply with fleet reconcile",
	))
	body.WriteString("\n\n")

	for index, machine := range m.machines {
		label := fmt.Sprintf("%s %s %d/%d",
			machine.ID,
			refreshSymbol(machine.RefreshStatus),
			machine.Plan.Summary.Satisfied,
			machine.Plan.Summary.Satisfied+machine.Plan.Summary.Missing+
				machine.Plan.Summary.Drifted+machine.Plan.Summary.Ambiguous+
				machine.Plan.Summary.NotChecked,
		)
		if index == m.machineIndex {
			body.WriteString(activeTabStyle.Render(label))
		} else {
			body.WriteString(tabStyle.Render(label))
		}
	}
	body.WriteString("\n\n")

	machine := m.machines[m.machineIndex]
	body.WriteString(fmt.Sprintf(
		"Profiles: %s    Satisfied: %d    Missing: %d    Drifted: %d    Extras: %d    Unchecked: %d\n",
		strings.Join(machine.Profiles, ", "),
		machine.Plan.Summary.Satisfied,
		machine.Plan.Summary.Missing,
		machine.Plan.Summary.Drifted,
		machine.Plan.Summary.Extra,
		machine.Plan.Summary.NotChecked,
	))
	if machine.Plan.LinkSummary != nil {
		linkSummary := machine.Plan.LinkSummary
		total := linkSummary.Satisfied + linkSummary.Missing +
			linkSummary.Drifted + linkSummary.NotChecked
		body.WriteString(fmt.Sprintf(
			"Portable links: %d/%d satisfied    Missing: %d    Drifted: %d    Unchecked: %d\n",
			linkSummary.Satisfied,
			total,
			linkSummary.Missing,
			linkSummary.Drifted,
			linkSummary.NotChecked,
		))
		for _, finding := range machine.Plan.LinkFindings {
			if finding.Status == model.LinkFindingSatisfied {
				continue
			}
			body.WriteString(fmt.Sprintf(
				"  %-14s  %-30s  %s\n",
				finding.Status,
				finding.LinkID,
				finding.Detail,
			))
		}
	}
	snapshotLabel := "unknown"
	if !machine.CollectedAt.IsZero() {
		snapshotLabel = machine.CollectedAt.Local().Format("2006-01-02 15:04:05")
	}
	body.WriteString(fmt.Sprintf(
		"Snapshot: %s    Refresh: %s\n",
		snapshotLabel,
		refreshDescription(machine),
	))
	body.WriteString(fmt.Sprintf(
		"Filter: %s    Showing: %d\n\n",
		filters[m.filterIndex],
		len(m.filteredFindings()),
	))

	findings := m.filteredFindings()
	if len(findings) == 0 {
		body.WriteString(mutedStyle.Render("No findings match this filter."))
		body.WriteString("\n")
	} else {
		visibleRows := m.visibleRows()
		start := 0
		if m.cursor >= visibleRows {
			start = m.cursor - visibleRows + 1
		}
		end := min(len(findings), start+visibleRows)
		for index := start; index < end; index++ {
			finding := findings[index]
			line := m.renderFinding(machine.ID, finding)
			if index == m.cursor {
				line = selectedStyle.Width(max(1, m.width-2)).Render(line)
			}
			body.WriteString(line)
			body.WriteString("\n")
		}
	}

	if selected, ok := m.selectedFinding(); ok {
		body.WriteString("\n")
		body.WriteString(detailStyle.Width(max(20, m.width-4)).Render(
			m.renderDetail(machine.ID, selected),
		))
		body.WriteString("\n")
	}

	if m.preview != nil {
		body.WriteString("\n")
		body.WriteString(detailStyle.Width(max(20, m.width-4)).Render(
			m.renderReconciliationPreview(),
		))
		body.WriteString("\n")
	}

	body.WriteString("\n")
	switch {
	case m.running:
		body.WriteString(mutedStyle.Render(
			"Reconciliation is running in the attached terminal…",
		))
	case m.previewing:
		body.WriteString(mutedStyle.Render("Preparing reconciliation preview…"))
	case m.confirming:
		body.WriteString(mutedStyle.Render(
			"y apply these reviewed changes   n/esc cancel   q quit",
		))
	default:
		body.WriteString(mutedStyle.Render(
			"[ / ] machine   j/k move   f filter   e extras   a sync to fleet   p keep local   i ignore   x uninstall here   c clear   r reconcile   q quit",
		))
	}
	if m.statusMessage != "" {
		body.WriteString("\n")
		body.WriteString(m.statusMessage)
	}

	view := tea.NewView(body.String())
	view.AltScreen = true
	view.WindowTitle = "envctl fleet"
	return view
}

func (m *Model) previewReconciliation() tea.Cmd {
	if m.reconciler == nil {
		m.statusMessage = "Reconciliation is unavailable in this session"
		return nil
	}
	if len(m.machines) == 0 {
		return nil
	}
	m.previewing = true
	m.preview = nil
	m.statusMessage = ""
	machineID := m.machines[m.machineIndex].ID
	return func() tea.Msg {
		plan, err := m.reconciler.Preview(machineID)
		return reconcilePreviewMsg{plan: plan, err: err}
	}
}

func (m *Model) runReconciliation() tea.Cmd {
	if m.reconciler == nil || m.preview == nil {
		m.statusMessage = "No reconciliation preview is available"
		return nil
	}
	command, err := m.reconciler.Command(m.preview.MachineID)
	if err != nil {
		m.statusMessage = "Could not start reconciliation: " + err.Error()
		return nil
	}
	m.running = true
	return m.launchProcess(command, func(err error) tea.Msg {
		return reconcileFinishedMsg{err: err}
	})
}

func (m *Model) renderReconciliationPreview() string {
	if m.preview == nil {
		return ""
	}
	lines := []string{
		fmt.Sprintf(
			"Reconcile reviewed extras on %s → profile %s",
			m.preview.MachineID, m.preview.Profile,
		),
	}
	for _, action := range m.preview.Actions {
		if action.Status != fleetreconcile.StatusPlanned &&
			action.Status != fleetreconcile.StatusBlocked {
			continue
		}
		label := strings.ToUpper(action.Decision)
		target := action.Installed.Package
		if action.Decision == "adopt" {
			target = action.PackageID + " → " + action.Profile
		}
		lines = append(lines, fmt.Sprintf(
			"  %-7s %-32s %s", label, target, action.Status,
		))
		if action.Command != nil {
			lines = append(lines, "          command: "+
				displayCommand(action.Command.Name, action.Command.Args))
		}
		if action.Status == fleetreconcile.StatusBlocked {
			lines = append(lines, "          "+action.Detail)
		}
	}
	switch {
	case len(m.preview.Blockers) > 0:
		lines = append(lines, fmt.Sprintf(
			"Blocked: %d issue(s) must be reviewed first",
			len(m.preview.Blockers),
		))
	case len(plannedActions(m.preview.Actions)) == 0:
		lines = append(lines, "No reviewed changes are ready to reconcile.")
	default:
		lines = append(lines,
			"Press y to apply and verify, or n to cancel.")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) applySuccessfulPreview() {
	if m.preview == nil {
		return
	}
	resolved := make(map[string]fleetreconcile.Action)
	for _, action := range m.preview.Actions {
		if action.Status == fleetreconcile.StatusPlanned {
			resolved[action.InventoryKey] = action
			delete(m.decisions, decisionMapKey(action.MachineID, action.InventoryKey))
		}
	}
	for machineIndex := range m.machines {
		if m.machines[machineIndex].ID != m.preview.MachineID {
			continue
		}
		findings := m.machines[machineIndex].Plan.Findings
		kept := make([]model.Finding, 0, len(findings))
		for _, finding := range findings {
			if finding.Status != model.FindingExtra || len(finding.Installed) == 0 {
				kept = append(kept, finding)
				continue
			}
			action, ok := resolved[InventoryKey(finding.Installed[0])]
			if !ok {
				kept = append(kept, finding)
				continue
			}
			if m.machines[machineIndex].Plan.Summary.Extra > 0 {
				m.machines[machineIndex].Plan.Summary.Extra--
			}
			if action.Decision == "adopt" {
				m.machines[machineIndex].Plan.Summary.Satisfied++
			}
		}
		m.machines[machineIndex].Plan.Findings = kept
		break
	}
	m.preview = nil
	m.confirming = false
	m.cursor = 0
}

func plannedActions(actions []fleetreconcile.Action) []fleetreconcile.Action {
	var planned []fleetreconcile.Action
	for _, action := range actions {
		if action.Status == fleetreconcile.StatusPlanned {
			planned = append(planned, action)
		}
	}
	return planned
}

func displayCommand(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

func (m *Model) filteredFindings() []model.Finding {
	if len(m.machines) == 0 {
		return nil
	}
	filter := filters[m.filterIndex]
	var findings []model.Finding
	for _, finding := range m.machines[m.machineIndex].Plan.Findings {
		include := false
		switch filter {
		case "attention":
			include = finding.Status != model.FindingSatisfied
		case "missing":
			include = finding.Status == model.FindingMissing ||
				finding.Status == model.FindingSourceDrift ||
				finding.Status == model.FindingKindDrift ||
				finding.Status == model.FindingVersionDrift ||
				finding.Status == model.FindingAmbiguous
		case "extra":
			include = finding.Status == model.FindingExtra
		case "not-checked":
			include = finding.Status == model.FindingNotChecked
		case "all":
			include = true
		}
		if include {
			findings = append(findings, finding)
		}
	}
	return findings
}

func (m *Model) selectedFinding() (model.Finding, bool) {
	findings := m.filteredFindings()
	if len(findings) == 0 || m.cursor >= len(findings) {
		return model.Finding{}, false
	}
	return findings[m.cursor], true
}

func (m *Model) saveSelectedDecision(value string) tea.Cmd {
	finding, ok := m.selectedFinding()
	if !ok || finding.Status != model.FindingExtra || len(finding.Installed) == 0 {
		m.statusMessage = "Decisions apply only to extra installed packages"
		return nil
	}
	machine := m.machines[m.machineIndex]
	key := InventoryKey(finding.Installed[0])
	profile := ""
	for _, candidate := range machine.Profiles {
		if candidate == "shared" {
			profile = candidate
			break
		}
	}
	if profile == "" && len(machine.Profiles) > 0 {
		profile = machine.Profiles[len(machine.Profiles)-1]
	}
	return func() tea.Msg {
		err := m.writer.SaveDecision(machine.ID, key, value, profile)
		return decisionSavedMsg{
			machineID: machine.ID, inventoryKey: key, value: value, err: err,
		}
	}
}

func (m *Model) moveMachine(delta int) {
	if len(m.machines) == 0 {
		return
	}
	m.machineIndex = (m.machineIndex + delta + len(m.machines)) % len(m.machines)
	m.cursor = 0
	m.statusMessage = ""
}

func (m *Model) moveCursor(delta int) {
	findings := m.filteredFindings()
	if len(findings) == 0 {
		m.cursor = 0
		return
	}
	m.cursor = (m.cursor + delta + len(findings)) % len(findings)
	m.statusMessage = ""
}

func (m *Model) clampCursor() {
	findings := m.filteredFindings()
	if len(findings) == 0 {
		m.cursor = 0
		return
	}
	m.cursor = min(m.cursor, len(findings)-1)
}

func (m *Model) visibleRows() int {
	return max(3, m.height-18)
}

func (m *Model) renderFinding(machineID string, finding model.Finding) string {
	status := statusLabel(finding.Status)
	name := finding.PackageID
	version := ""
	decision := ""
	if len(finding.Installed) > 0 {
		item := finding.Installed[0]
		if name == "" {
			name = item.Package
		}
		version = item.Version
		decision = m.decisions[decisionMapKey(machineID, InventoryKey(item))]
	}
	line := fmt.Sprintf("%-11s  %-30s", status, name)
	if version != "" {
		line += "  " + version
	}
	if decision != "" {
		line += "  [" + decision + "]"
	}
	return line
}

func (m *Model) renderDetail(machineID string, finding model.Finding) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("%s: %s", finding.Status, finding.Detail))
	if finding.Desired != nil {
		lines = append(lines, fmt.Sprintf(
			"Desired: %s / %s / %s / %s",
			finding.Desired.Manager,
			finding.Desired.Kind,
			finding.Desired.Source,
			finding.Desired.Package,
		))
	}
	for _, item := range finding.Installed {
		lines = append(lines, fmt.Sprintf(
			"Installed: %s / %s / %s / %s  %s",
			item.Manager, item.Kind, item.Source, item.Package, item.Version,
		))
		if decision := m.decisions[decisionMapKey(machineID, InventoryKey(item))]; decision != "" {
			lines = append(lines, "Decision: "+decision)
			switch decision {
			case "adopt":
				lines = append(lines,
					"Effect: add to the shared desired-state profile for the other Macs")
			case "remove":
				lines = append(lines,
					"Effect: uninstall only from this Mac after dry-run and confirmation")
			}
		}
	}
	return strings.Join(lines, "\n")
}

func InventoryKey(item model.InstalledPackage) string {
	return fleetreconcile.InventoryKey(item)
}

func decisionMapKey(machineID, inventoryKey string) string {
	return machineID + "\x00" + inventoryKey
}

func statusLabel(status model.FindingStatus) string {
	switch status {
	case model.FindingSatisfied:
		return "ok"
	case model.FindingMissing:
		return "missing"
	case model.FindingSourceDrift, model.FindingKindDrift,
		model.FindingVersionDrift:
		return "drift"
	case model.FindingAmbiguous:
		return "ambiguous"
	case model.FindingExtra:
		return "EXTRA"
	case model.FindingNotChecked:
		return "unchecked"
	default:
		return string(status)
	}
}

func filterIndex(name string) int {
	for index, filter := range filters {
		if filter == name {
			return index
		}
	}
	return 0
}

func refreshSymbol(status string) string {
	switch status {
	case "ok":
		return "✓"
	case "error":
		return "!"
	default:
		return "·"
	}
}

func refreshDescription(machine Machine) string {
	switch machine.RefreshStatus {
	case "ok":
		return "ok"
	case "error":
		if machine.RetainedLastGood {
			return "failed; showing last good snapshot"
		}
		return "failed"
	default:
		return "saved snapshot"
	}
}
