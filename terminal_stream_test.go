package agentcli

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestTerminalStreamRendererReplacesPartialLineWithoutDuplicates(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	var output bytes.Buffer
	screen := newTerminalTestScreen()

	for _, fragment := range []string{"I'm", " your primary", " agent for this session."} {
		before := output.Len()
		if !renderer.write(&output, fragment) {
			t.Fatal("interactive renderer did not handle provider fragment")
		}
		screen.apply(output.Bytes()[before:])
	}

	if source, _ := terminalRendererSnapshot(renderer); source != "I'm your primary agent for this session." {
		t.Fatalf("stream source = %q", source)
	}
	if got := terminalANSIEscape.ReplaceAllString(screen.text(), ""); !strings.Contains(got, "I'm your primary agent for this session.") {
		t.Fatalf("rendered screen = %q", got)
	}
	if got := strings.Count(screen.text(), "I'm"); got != 1 {
		t.Fatalf("first fragment remained on %d screen lines, want exactly one", got)
	}
}

func TestTerminalStreamRendererCommitsCompletedLines(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	var output bytes.Buffer
	screen := newTerminalTestScreen()

	for _, fragment := range []string{"First", " line\nSec", "ond line"} {
		before := output.Len()
		renderer.write(&output, fragment)
		screen.apply(output.Bytes()[before:])
	}
	if got := terminalANSIEscape.ReplaceAllString(screen.text(), ""); !strings.Contains(got, "First line") || !strings.Contains(got, "Second line") {
		t.Fatalf("rendered screen = %q", got)
	}
	if !renderer.commit(&output) {
		t.Fatal("active stream was not committed")
	}
	if renderer.commit(&output) {
		t.Fatal("inactive stream committed twice")
	}

	before := output.Len()
	renderer.write(&output, "New response")
	newWrite := output.String()[before:]
	if strings.Contains(newWrite, "\x1b[1A") {
		t.Fatalf("new response erased committed output: %q", newWrite)
	}
}

func TestTerminalStreamRendererFallsBackWithoutReadline(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	var output bytes.Buffer
	if renderer.write(&output, "answer") {
		t.Fatal("detached renderer unexpectedly handled output")
	}
	if output.Len() != 0 {
		t.Fatalf("detached renderer wrote %q", output.String())
	}
}

func TestTerminalStreamRendererLeavesSpaceAboveInput(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	var output bytes.Buffer

	renderer.write(&output, "answer")

	if !strings.HasSuffix(output.String(), "\n\n") {
		t.Fatalf("stream output does not leave a blank row above input: %q", output.String())
	}
}

func TestTerminalStreamRendererFormatsMarkdownWithoutVerbosePadding(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	renderer.source = "### Title\n\nThis is **bold** with `inline code`.\n\n- one\n- two\n\n```go\nfmt.Println(\"hi\")\n```"

	renderer.mu.Lock()
	rendered := renderer.renderMarkdownLocked()
	renderer.mu.Unlock()
	plain := terminalANSIEscape.ReplaceAllString(rendered, "")
	for _, wanted := range []string{"Title", "This is bold with inline code.", "• one", "fmt.Println"} {
		if !strings.Contains(plain, wanted) {
			t.Fatalf("rendered Markdown %q missing %q", plain, wanted)
		}
	}
	for _, rawSyntax := range []string{"# Title", "###", "**bold**", "```go"} {
		if strings.Contains(plain, rawSyntax) {
			t.Fatalf("rendered Markdown still contains %q: %q", rawSyntax, plain)
		}
	}
	if strings.Contains(rendered, "\x1b[48;") {
		t.Fatalf("rendered Markdown contains a background color: %q", rendered)
	}
	if !strings.Contains(rendered, "\x1b[38;5;203") {
		t.Fatalf("inline code did not keep its red foreground: %q", rendered)
	}
	if len(rendered) > 4096 {
		t.Fatalf("small Markdown rendered to %d bytes", len(rendered))
	}
}

func TestTerminalMarkdownStyleUsesOneDarkForCodeBlocks(t *testing.T) {
	style := terminalMarkdownStyleConfig()
	if style.CodeBlock.Theme != "onedark" {
		t.Fatalf("code block theme = %q, want onedark", style.CodeBlock.Theme)
	}
	if style.CodeBlock.Chroma != nil {
		t.Fatal("embedded code block palette overrides the One Dark theme")
	}
}

func TestTerminalStreamRendererCommitsStablePrefixForLongResponses(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 100 })
	var output bytes.Buffer
	var firstFragment strings.Builder
	firstFragment.WriteString("```go\n")
	for index := range 100 {
		firstFragment.WriteString(fmt.Sprintf("fmt.Println(%d)\n", index))
	}

	renderer.write(&output, firstFragment.String())
	before := output.Len()
	renderer.write(&output, "fmt.Println(100)\n")
	update := output.String()[before:]

	if renderer.committedLines < 80 {
		t.Fatalf("committed lines = %d, want the stable prefix in scrollback", renderer.committedLines)
	}
	if moves := strings.Count(update, "\x1b[1A"); moves > terminalStreamStableTailLines+2 {
		t.Fatalf("long response redraw moved up %d rows; update can escape the viewport", moves)
	}
	if strings.Contains(update, "fmt.Println(0)") {
		t.Fatalf("long response redrew its committed prefix: %q", update)
	}
}

func TestTerminalStreamRowsCountsWrappingAndWideRunes(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  int
	}{
		{name: "empty", value: "", width: 5, want: 0},
		{name: "one row", value: "12345", width: 5, want: 1},
		{name: "wrapped", value: "123456", width: 5, want: 2},
		{name: "wide runes", value: "日本語", width: 4, want: 2},
		{name: "color ignored", value: "\x1b[31mhello\x1b[0m", width: 5, want: 1},
		{name: "multiple lines", value: "one\ntwo\nthree", width: 20, want: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalStreamRows(test.value, test.width); got != test.want {
				t.Fatalf("terminalStreamRows(%q, %d) = %d, want %d", test.value, test.width, got, test.want)
			}
		})
	}
}

func BenchmarkTerminalStreamRendererMarkdown(b *testing.B) {
	fragment := "A short **Markdown** fragment with `code`.\n"
	for range b.N {
		renderer := &terminalStreamRenderer{}
		renderer.attach(func() int { return 100 })
		for range 100 {
			renderer.write(io.Discard, fragment)
		}
	}
}

type terminalTestScreen struct {
	lines [][]rune
	row   int
	col   int
}

func newTerminalTestScreen() *terminalTestScreen {
	return &terminalTestScreen{lines: [][]rune{{}}}
}

func (screen *terminalTestScreen) apply(value []byte) {
	for index := 0; index < len(value); {
		switch {
		case bytes.HasPrefix(value[index:], []byte("\x1b[1A")):
			if screen.row > 0 {
				screen.row--
			}
			index += len("\x1b[1A")
		case bytes.HasPrefix(value[index:], []byte("\x1b[2K")):
			screen.ensureRow()
			screen.lines[screen.row] = nil
			screen.col = 0
			index += len("\x1b[2K")
		case value[index] == '\r':
			screen.col = 0
			index++
		case value[index] == '\n':
			screen.row++
			screen.col = 0
			screen.ensureRow()
			index++
		default:
			current := rune(value[index])
			screen.ensureRow()
			for len(screen.lines[screen.row]) <= screen.col {
				screen.lines[screen.row] = append(screen.lines[screen.row], ' ')
			}
			screen.lines[screen.row][screen.col] = current
			screen.col++
			index++
		}
	}
}

func (screen *terminalTestScreen) ensureRow() {
	for len(screen.lines) <= screen.row {
		screen.lines = append(screen.lines, nil)
	}
}

func (screen *terminalTestScreen) text() string {
	lines := make([]string, len(screen.lines))
	for index, line := range screen.lines {
		lines[index] = strings.TrimRight(string(line), " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}
