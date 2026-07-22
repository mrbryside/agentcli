package agentcli

import (
	"bytes"
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

	if got, want := screen.text(), "I'm your primary agent for this session."; got != want {
		t.Fatalf("rendered screen = %q, want %q", got, want)
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
	if got, want := screen.text(), "First line\nSecond line"; got != want {
		t.Fatalf("rendered screen = %q, want %q", got, want)
	}
	if !renderer.commit() {
		t.Fatal("active stream was not committed")
	}
	if renderer.commit() {
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalStreamRows(test.value, test.width); got != test.want {
				t.Fatalf("terminalStreamRows(%q, %d) = %d, want %d", test.value, test.width, got, test.want)
			}
		})
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
