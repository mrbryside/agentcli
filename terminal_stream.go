package agentcli

import (
	"io"
	"regexp"
	"strings"
	"sync"

	readlinerunes "github.com/chzyer/readline/runes"
)

const terminalStreamFallbackWidth = 80

var terminalANSIEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

// terminalStreamRenderer keeps only the assistant's current, incomplete line.
// Each provider fragment redraws that line in place through readline's writer.
// Completed lines are left in the terminal scrollback and never redrawn.
//
// Readline temporarily removes and restores the editable prompt around every
// Write. Ending each render on the line above that prompt lets users continue
// typing queued input while provider content streams without being erased.
type terminalStreamRenderer struct {
	mu           sync.Mutex
	attached     bool
	active       bool
	partial      string
	renderedRows int
	width        func() int
}

func (renderer *terminalStreamRenderer) attach(width func() int) {
	if renderer == nil {
		return
	}
	renderer.mu.Lock()
	renderer.attached = true
	renderer.active = false
	renderer.partial = ""
	renderer.renderedRows = 0
	renderer.width = width
	renderer.mu.Unlock()
}

func (renderer *terminalStreamRenderer) detach() {
	if renderer == nil {
		return
	}
	renderer.mu.Lock()
	renderer.attached = false
	renderer.active = false
	renderer.partial = ""
	renderer.renderedRows = 0
	renderer.width = nil
	renderer.mu.Unlock()
}

// write returns false when no interactive readline prompt is attached. The
// caller can then use an ordinary writer without terminal cursor control.
func (renderer *terminalStreamRenderer) write(output io.Writer, fragment string) bool {
	if renderer == nil || fragment == "" {
		return renderer != nil && fragment == ""
	}
	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	if !renderer.attached {
		return false
	}

	combined := renderer.partial + fragment
	var rendered strings.Builder
	for range renderer.renderedRows {
		rendered.WriteString("\x1b[1A\r\x1b[2K")
	}
	rendered.WriteString(combined)
	if !strings.HasSuffix(combined, "\n") {
		rendered.WriteByte('\n')
	}
	_, _ = io.WriteString(output, rendered.String())

	renderer.active = true
	if newline := strings.LastIndexByte(combined, '\n'); newline >= 0 {
		renderer.partial = combined[newline+1:]
	} else {
		renderer.partial = combined
	}
	renderer.renderedRows = terminalStreamRows(renderer.partial, renderer.currentWidth())
	return true
}

// commit makes the displayed partial line ordinary terminal scrollback. No
// bytes are needed because write always leaves readline's prompt below it.
func (renderer *terminalStreamRenderer) commit() bool {
	if renderer == nil {
		return false
	}
	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	active := renderer.attached && renderer.active
	renderer.active = false
	renderer.partial = ""
	renderer.renderedRows = 0
	return active
}

func (renderer *terminalStreamRenderer) reset() {
	if renderer == nil {
		return
	}
	renderer.mu.Lock()
	renderer.active = false
	renderer.partial = ""
	renderer.renderedRows = 0
	renderer.mu.Unlock()
}

func (renderer *terminalStreamRenderer) currentWidth() int {
	if renderer.width == nil {
		return terminalStreamFallbackWidth
	}
	if width := renderer.width(); width > 0 {
		return width
	}
	return terminalStreamFallbackWidth
}

func terminalStreamRows(value string, width int) int {
	if value == "" {
		return 0
	}
	if width <= 0 {
		width = terminalStreamFallbackWidth
	}
	visible := []rune(terminalANSIEscape.ReplaceAllString(value, ""))
	rows := 1
	column := 0
	for _, current := range visible {
		cellWidth := readlinerunes.Width(current)
		if current == '\t' {
			cellWidth = 4 - column%4
		}
		if cellWidth <= 0 {
			continue
		}
		if column > 0 && column+cellWidth > width {
			rows++
			column = 0
		}
		column += cellWidth
	}
	return rows
}
