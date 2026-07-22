package agentcli

import (
	"io"
	"regexp"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	readlinerunes "github.com/chzyer/readline/runes"
)

const terminalStreamFallbackWidth = 80

var terminalANSIEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

var terminalMarkdownStyle = func() glamour.TermRendererOption {
	style := styles.DarkStyleConfig
	zero := uint(0)
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""
	style.Document.Color = nil
	style.Document.Margin = &zero
	headings := []*string{
		&style.H1.Prefix,
		&style.H2.Prefix,
		&style.H3.Prefix,
		&style.H4.Prefix,
		&style.H5.Prefix,
		&style.H6.Prefix,
	}
	for _, prefix := range headings {
		*prefix = ""
	}
	style.H1.Suffix = ""
	style.H1.BackgroundColor = nil
	style.Code.Prefix = ""
	style.Code.Suffix = ""
	style.Code.BackgroundColor = nil
	style.CodeBlock.Margin = &zero
	return glamour.WithStyles(style)
}()

// terminalStreamRenderer is the interactive terminal's live view state. Each
// provider event appends Markdown source, then redraws that complete rendered
// document above readline's independently managed input row. Loading status is
// another row in the same view, so it can never become part of the prompt.
type terminalStreamRenderer struct {
	mu               sync.Mutex
	attached         bool
	active           bool
	source           string
	status           string
	renderedRows     int
	width            func() int
	markdownWidth    int
	markdownRenderer *glamour.TermRenderer
}

func (renderer *terminalStreamRenderer) attach(width func() int) {
	if renderer == nil {
		return
	}
	renderer.mu.Lock()
	renderer.attached = true
	renderer.active = false
	renderer.source = ""
	renderer.status = ""
	renderer.renderedRows = 0
	renderer.width = width
	renderer.markdownWidth = 0
	renderer.markdownRenderer = nil
	renderer.mu.Unlock()
}

func (renderer *terminalStreamRenderer) detach() {
	if renderer == nil {
		return
	}
	renderer.mu.Lock()
	renderer.attached = false
	renderer.active = false
	renderer.source = ""
	renderer.status = ""
	renderer.renderedRows = 0
	renderer.width = nil
	renderer.markdownWidth = 0
	renderer.markdownRenderer = nil
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
	renderer.source += fragment
	renderer.renderLocked(output)
	return true
}

func (renderer *terminalStreamRenderer) setStatus(output io.Writer, status string) bool {
	if renderer == nil {
		return false
	}
	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	if !renderer.attached {
		return false
	}
	status = strings.TrimSpace(status)
	if renderer.status == status {
		return true
	}
	renderer.status = status
	renderer.renderLocked(output)
	return true
}

// commit leaves the final Markdown in scrollback and removes transient status.
func (renderer *terminalStreamRenderer) commit(output io.Writer) bool {
	if renderer == nil {
		return false
	}
	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	active := renderer.attached && renderer.active
	if renderer.attached && renderer.status != "" {
		renderer.status = ""
		renderer.renderLocked(output)
	}
	renderer.active = false
	renderer.source = ""
	renderer.status = ""
	renderer.renderedRows = 0
	return active
}

func (renderer *terminalStreamRenderer) reset() {
	if renderer == nil {
		return
	}
	renderer.mu.Lock()
	renderer.active = false
	renderer.source = ""
	renderer.status = ""
	renderer.renderedRows = 0
	renderer.mu.Unlock()
}

func (renderer *terminalStreamRenderer) renderLocked(output io.Writer) {
	display := renderer.renderMarkdownLocked()
	if renderer.status != "" {
		if display != "" {
			display += "\n"
		}
		display += renderer.status
	}

	var update strings.Builder
	for range renderer.renderedRows {
		update.WriteString("\x1b[1A\r\x1b[2K")
	}
	if display != "" {
		update.WriteString(display)
		update.WriteByte('\n')
	}
	_, _ = io.WriteString(output, update.String())

	renderer.active = display != ""
	renderer.renderedRows = terminalStreamRows(display, renderer.currentWidth())
}

func (renderer *terminalStreamRenderer) renderMarkdownLocked() string {
	if renderer.source == "" {
		return ""
	}
	width := renderer.currentWidth()
	if renderer.markdownRenderer == nil || renderer.markdownWidth != width {
		markdownRenderer, err := glamour.NewTermRenderer(
			terminalMarkdownStyle,
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return strings.Trim(renderer.source, "\n")
		}
		renderer.markdownRenderer = markdownRenderer
		renderer.markdownWidth = width
	}
	rendered, err := renderer.markdownRenderer.Render(renderer.source)
	if err != nil {
		return strings.Trim(renderer.source, "\n")
	}
	return trimTerminalLinePadding(strings.Trim(rendered, "\n"))
}

func trimTerminalLinePadding(value string) string {
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	return strings.Join(lines, "\n")
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
		if current == '\n' {
			rows++
			column = 0
			continue
		}
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
