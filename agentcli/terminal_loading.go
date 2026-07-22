package agentcli

import (
	"strings"
	"sync"
	"time"
)

var terminalLoadingFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const terminalLoadingInterval = 90 * time.Millisecond

type terminalPromptEditor interface {
	SetPrompt(string)
	Refresh()
}

type terminalLoadingState struct {
	mu         sync.Mutex
	editor     terminalPromptEditor
	basePrompt string
	generation uint64
	active     bool
	stop       chan struct{}
	label      string
	color      bool
}

type terminalLoadingHandle struct {
	state      *terminalLoadingState
	generation uint64
}

type terminalLoadingController struct {
	mu       sync.Mutex
	terminal terminal
	handle   terminalLoadingHandle
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
	controller.handle = controller.terminal.startLoading(label)
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

func (t terminal) startLoading(label string) terminalLoadingHandle {
	if !t.interactive || t.loading == nil {
		return terminalLoadingHandle{}
	}
	return t.loading.start(label, t.color)
}

func (state *terminalLoadingState) attach(editor terminalPromptEditor, basePrompt string) {
	if state == nil || editor == nil {
		return
	}
	state.mu.Lock()
	state.editor = editor
	state.basePrompt = basePrompt
	state.mu.Unlock()
}

func (state *terminalLoadingState) detach(editor terminalPromptEditor) {
	if state == nil {
		return
	}
	state.stopCurrent()
	state.mu.Lock()
	if state.editor == editor {
		state.editor = nil
	}
	state.mu.Unlock()
}

func (state *terminalLoadingState) start(label string, color bool) terminalLoadingHandle {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Working"
	}
	state.mu.Lock()
	if state.editor == nil {
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
	state.label = label
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
	prefix := terminalLoadingFrames[frame] + " " + state.label + " · "
	if state.color {
		prefix = "\033[36m" + terminalLoadingFrames[frame] + "\033[0m \033[2m" + state.label + " ·\033[0m "
	}
	state.editor.SetPrompt(prefix + state.basePrompt)
	state.editor.Refresh()
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
	if state.editor != nil {
		state.editor.SetPrompt(state.basePrompt)
		state.editor.Refresh()
	}
}
