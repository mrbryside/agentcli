package agentcli

import (
	"bytes"
	"testing"
)

func TestTerminalInputShiftEnterBuildsMultilineDraft(t *testing.T) {
	editor, _ := newTerminalInputEditorForTest()
	editor.pending = append(editor.pending, []byte("first")...)
	editor.consumePending()
	editor.pending = append(editor.pending, terminalShiftEnterKeys[0]...)
	editor.consumePending()
	editor.pending = append(editor.pending, []byte("second\r")...)
	editor.consumePending()

	if got := <-editor.lines; got != "first\nsecond" {
		t.Fatalf("submitted input = %q, want multiline draft", got)
	}
}

func TestTerminalInputBracketedPasteStaysOneDraft(t *testing.T) {
	editor, _ := newTerminalInputEditorForTest()
	editor.pending = append(editor.pending, terminalBracketedPasteStart...)
	editor.pending = append(editor.pending, []byte("first\r\nsecond\nthird")...)
	editor.pending = append(editor.pending, terminalBracketedPasteEnd...)
	editor.pending = append(editor.pending, '\r')
	editor.consumePending()

	if got := <-editor.lines; got != "first\nsecond\nthird" {
		t.Fatalf("pasted input = %q, want one normalized multiline draft", got)
	}
	select {
	case extra := <-editor.lines:
		t.Fatalf("paste submitted an extra message %q", extra)
	default:
	}
}

func TestTerminalInputControlOTogglesReasoning(t *testing.T) {
	editor, _ := newTerminalInputEditorForTest()
	editor.pending = append(editor.pending, byte(15))
	editor.consumePending()

	select {
	case <-editor.reasoningToggles:
	default:
		t.Fatal("Ctrl+O did not emit a reasoning toggle")
	}
}

func TestTerminalInputEditsAtCursorAndRestoresHistory(t *testing.T) {
	editor, _ := newTerminalInputEditorForTest()
	editor.pending = append(editor.pending, []byte("ac")...)
	editor.pending = append(editor.pending, terminalLeftKeys[0]...)
	editor.pending = append(editor.pending, []byte("b\r")...)
	editor.consumePending()
	if got := <-editor.lines; got != "abc" {
		t.Fatalf("cursor-edited input = %q", got)
	}

	editor.pending = append(editor.pending, terminalUpKeys[0]...)
	editor.consumePending()
	if got := string(editor.buffer); got != "abc" {
		t.Fatalf("history input = %q", got)
	}
}

func TestTerminalInputDisplayUsesContinuationLines(t *testing.T) {
	display := terminalInputDisplay("❯ ", []rune("first\nsecond"))
	if display != "❯ first\n  second" {
		t.Fatalf("input display = %q", display)
	}
}

func newTerminalInputEditorForTest() (*terminalInputEditor, *bytes.Buffer) {
	output := &bytes.Buffer{}
	editor := &terminalInputEditor{
		output:           output,
		prompt:           "❯ ",
		lines:            make(chan string, 8),
		errors:           make(chan error, 1),
		escapes:          make(chan struct{}, 1),
		reasoningToggles: make(chan struct{}, 1),
		stop:             make(chan struct{}),
		historyIndex:     0,
	}
	editor.renderInputLocked()
	return editor, output
}
