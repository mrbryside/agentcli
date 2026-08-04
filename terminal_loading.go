package agentcli

import (
	"io"
	"strings"
	"sync"
	"time"
)

var terminalLoadingFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const terminalLoadingInterval = 90 * time.Millisecond

type terminalLoadingState struct {
	mu         sync.Mutex
	renderer   *terminalStreamRenderer
	output     io.Writer
	generation uint64
	active     bool
	stop       chan struct{}
	rows       []terminalLoadingRow
	color      bool
}

type terminalLoadingRow struct {
	label   string
	running bool
	icon    string
	color   string
}

type terminalTaskLoadingEntry struct {
	agent string
	row   terminalLoadingRow
}

type terminalLoadingHandle struct {
	state      *terminalLoadingState
	generation uint64
}

type terminalLoadingController struct {
	mu        sync.Mutex
	terminal  terminal
	handle    terminalLoadingHandle
	taskOrder []string
	tasks     map[string]terminalTaskLoadingEntry
}

func (t terminal) loadingController() *terminalLoadingController {
	return &terminalLoadingController{terminal: t}
}

func (t terminal) stopLoading() {
	if t.loading != nil {
		t.loading.stopCurrent()
	}
}

func (controller *terminalLoadingController) Start(label string) {
	if controller == nil {
		return
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	rows := controller.taskRowsLocked()
	if len(rows) == 0 || strings.TrimSpace(label) != "" {
		rows = append(rows, terminalLoadingRow{label: label, running: true})
	}
	controller.handle = controller.terminal.startLoadingRows(rows)
}

func (controller *terminalLoadingController) Stop() {
	if controller == nil {
		return
	}
	controller.mu.Lock()
	handle := controller.handle
	controller.handle = terminalLoadingHandle{}
	controller.mu.Unlock()
	handle.stop()
}

// StartTask replaces the generic loading row with one independently animated
// row per task call. The call ID keeps completion updates correlated when
// concurrent task results arrive out of order. Hidden root views retain the
// rows without redrawing the currently selected view.
func (controller *terminalLoadingController) StartTask(callID, agent, activity string, visible bool) bool {
	if controller == nil || !controller.terminal.interactive || controller.terminal.loading == nil {
		return false
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	agent = strings.TrimSpace(agent)
	activity = strings.TrimSpace(activity)
	if agent == "" {
		agent = "task"
	}

	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.tasks == nil {
		controller.tasks = make(map[string]terminalTaskLoadingEntry)
	}
	if _, exists := controller.tasks[callID]; !exists {
		controller.taskOrder = append(controller.taskOrder, callID)
	}
	controller.tasks[callID] = terminalTaskLoadingEntry{
		agent: agent,
		row:   terminalLoadingRow{label: terminalTaskLoadingLabel(agent, activity), running: true},
	}
	if visible {
		controller.handle = controller.terminal.startLoadingRows(controller.taskRowsLocked())
	}
	return true
}

// FinishTask updates one task row without disturbing other running rows. Once
// every task call has returned, the final rows are committed to scrollback.
func (controller *terminalLoadingController) FinishTask(callID, agent string, state TaskState, visible bool) (handled, allDone bool) {
	if controller == nil {
		return false, false
	}
	callID = strings.TrimSpace(callID)
	controller.mu.Lock()
	defer controller.mu.Unlock()

	entry, exists := controller.tasks[callID]
	if !exists {
		return false, false
	}
	if agent = strings.TrimSpace(agent); agent != "" {
		entry.agent = agent
	}
	entry.row = terminalTaskFinishedRow(entry.agent, state)
	controller.tasks[callID] = entry

	rows := controller.taskRowsLocked()
	allDone = true
	for _, row := range rows {
		if row.running {
			allDone = false
			break
		}
	}
	if !allDone {
		if visible {
			controller.handle = controller.terminal.startLoadingRows(rows)
		}
		return true, false
	}

	if visible {
		handle := controller.handle
		controller.handle = terminalLoadingHandle{}
		handle.stop()
		controller.terminal.println(terminalLoadingRowsDisplay(rows, 0, controller.terminal.color))
	}
	controller.taskOrder = nil
	controller.tasks = nil
	return true, true
}

func (controller *terminalLoadingController) taskRowsLocked() []terminalLoadingRow {
	if len(controller.taskOrder) == 0 {
		return nil
	}
	rows := make([]terminalLoadingRow, 0, len(controller.taskOrder))
	for _, callID := range controller.taskOrder {
		entry, exists := controller.tasks[callID]
		if exists {
			rows = append(rows, entry.row)
		}
	}
	return rows
}

func terminalTaskLoadingLabel(agent, activity string) string {
	if activity == "" {
		return agent
	}
	return agent + " · " + activity
}

func terminalTaskFinishedRow(agent string, state TaskState) terminalLoadingRow {
	row := terminalLoadingRow{label: terminalTaskLoadingLabel(agent, string(state))}
	switch state {
	case TaskStateRunning:
		row.icon, row.color = "↗", "36"
	case TaskStateIncomplete:
		row.icon, row.color = "!", "33"
	case TaskStateError:
		row.icon, row.color = "✗", "31"
	default:
		row.icon, row.color = "✓", "32"
	}
	return row
}

func (t terminal) startLoadingRows(rows []terminalLoadingRow) terminalLoadingHandle {
	if !t.interactive || t.loading == nil {
		return terminalLoadingHandle{}
	}
	return t.loading.startRows(rows, t.color)
}

func (state *terminalLoadingState) attach(renderer *terminalStreamRenderer, output io.Writer) {
	if state == nil || renderer == nil || output == nil {
		return
	}
	state.mu.Lock()
	state.renderer = renderer
	state.output = output
	state.mu.Unlock()
}

func (state *terminalLoadingState) detach(renderer *terminalStreamRenderer) {
	if state == nil {
		return
	}
	state.stopCurrent()
	state.mu.Lock()
	if state.renderer == renderer {
		state.renderer = nil
		state.output = nil
	}
	state.mu.Unlock()
}

func (state *terminalLoadingState) startRows(rows []terminalLoadingRow, color bool) terminalLoadingHandle {
	rows = append([]terminalLoadingRow(nil), rows...)
	for index := range rows {
		rows[index].label = strings.TrimSpace(rows[index].label)
	}
	state.mu.Lock()
	if state.renderer == nil || state.output == nil {
		state.mu.Unlock()
		return terminalLoadingHandle{}
	}
	if state.active {
		close(state.stop)
	}
	state.generation++
	generation := state.generation
	state.active = true
	state.stop = make(chan struct{})
	state.rows = rows
	state.color = color
	stop := state.stop
	state.renderLocked(0)
	state.mu.Unlock()

	go state.animate(generation, stop)
	return terminalLoadingHandle{state: state, generation: generation}
}

func (state *terminalLoadingState) animate(generation uint64, stop <-chan struct{}) {
	ticker := time.NewTicker(terminalLoadingInterval)
	defer ticker.Stop()
	frame := 1
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			state.mu.Lock()
			if !state.active || state.generation != generation {
				state.mu.Unlock()
				return
			}
			state.renderLocked(frame % len(terminalLoadingFrames))
			frame++
			state.mu.Unlock()
		}
	}
}

func (state *terminalLoadingState) renderLocked(frame int) {
	state.renderer.setStatus(state.output, terminalLoadingRowsDisplay(state.rows, frame, state.color))
}

func terminalLoadingRowsDisplay(rows []terminalLoadingRow, frame int, color bool) string {
	lines := make([]string, 0, len(rows))
	for index, row := range rows {
		icon := row.icon
		iconColor := row.color
		if row.running {
			icon = terminalLoadingFrames[(frame+index)%len(terminalLoadingFrames)]
			iconColor = "36"
		}
		if icon == "" {
			continue
		}
		line := icon
		if color {
			line = "\033[" + iconColor + "m" + icon + "\033[0m"
		}
		if row.label != "" {
			if color {
				line += " \033[2m" + row.label + "\033[0m"
			} else {
				line += " " + row.label
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (state *terminalLoadingState) stopCurrent() {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.stopLocked(state.generation)
	state.mu.Unlock()
}

func (handle terminalLoadingHandle) stop() {
	if handle.state == nil {
		return
	}
	handle.state.mu.Lock()
	handle.state.stopLocked(handle.generation)
	handle.state.mu.Unlock()
}

func (state *terminalLoadingState) stopLocked(generation uint64) {
	if !state.active || state.generation != generation {
		return
	}
	state.active = false
	close(state.stop)
	if state.renderer != nil && state.output != nil {
		state.renderer.setStatus(state.output, "")
	}
}
