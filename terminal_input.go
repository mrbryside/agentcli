package agentcli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chzyer/readline"
	readlinerunes "github.com/chzyer/readline/runes"
)

const terminalInputEscapeDelay = 25 * time.Millisecond

var (
	terminalBracketedPasteStart = []byte("\x1b[200~")
	terminalBracketedPasteEnd   = []byte("\x1b[201~")
	terminalShiftEnterKeys      = [][]byte{
		[]byte("\x1b[13;2u"),
		[]byte("\x1b[27;2;13~"),
		[]byte("\x1b\r"),
	}
	terminalSubmitKeys = [][]byte{
		[]byte("\x1b[13u"),
	}
	terminalBackspaceKeys = [][]byte{
		[]byte("\x1b[127u"),
	}
	terminalEOFKeys = [][]byte{
		[]byte("\x1b[100;5u"),
	}
	terminalEscapeKeys = [][]byte{
		[]byte("\x1b[27u"),
	}
	terminalControlOKeys = [][]byte{
		[]byte("\x1b[111;5u"),
	}
	terminalInterruptKeys = [][]byte{
		[]byte("\x1b[99;5u"),
	}
	terminalLeftKeys = [][]byte{
		[]byte("\x1b[D"),
	}
	terminalRightKeys = [][]byte{
		[]byte("\x1b[C"),
	}
	terminalUpKeys = [][]byte{
		[]byte("\x1b[A"),
	}
	terminalDownKeys = [][]byte{
		[]byte("\x1b[B"),
	}
	terminalHomeKeys = [][]byte{
		[]byte("\x1b[H"),
	}
	terminalEndKeys = [][]byte{
		[]byte("\x1b[F"),
	}
	terminalDeleteKeys = [][]byte{
		[]byte("\x1b[3~"),
	}
	terminalIgnoredKeys = [][]byte{
		[]byte("\x1b[5~"),
		[]byte("\x1b[6~"),
	}
)

var terminalInputSequences = func() [][]byte {
	sequences := make([][]byte, 0, 1+len(terminalShiftEnterKeys)+len(terminalSubmitKeys)+len(terminalBackspaceKeys)+len(terminalEOFKeys)+len(terminalEscapeKeys)+len(terminalControlOKeys)+len(terminalInterruptKeys)+len(terminalLeftKeys)+len(terminalRightKeys)+len(terminalUpKeys)+len(terminalDownKeys)+len(terminalHomeKeys)+len(terminalEndKeys)+len(terminalDeleteKeys)+len(terminalIgnoredKeys))
	sequences = append(sequences, terminalBracketedPasteStart)
	sequences = append(sequences, terminalShiftEnterKeys...)
	sequences = append(sequences, terminalSubmitKeys...)
	sequences = append(sequences, terminalBackspaceKeys...)
	sequences = append(sequences, terminalEOFKeys...)
	sequences = append(sequences, terminalEscapeKeys...)
	sequences = append(sequences, terminalControlOKeys...)
	sequences = append(sequences, terminalInterruptKeys...)
	sequences = append(sequences, terminalLeftKeys...)
	sequences = append(sequences, terminalRightKeys...)
	sequences = append(sequences, terminalUpKeys...)
	sequences = append(sequences, terminalDownKeys...)
	sequences = append(sequences, terminalHomeKeys...)
	sequences = append(sequences, terminalEndKeys...)
	sequences = append(sequences, terminalDeleteKeys...)
	return append(sequences, terminalIgnoredKeys...)
}()

type terminalInputEditor struct {
	mu               sync.Mutex
	input            io.ReadCloser
	output           io.Writer
	prompt           string
	buffer           []rune
	cursor           int
	cursorRow        int
	renderedRows     int
	history          [][]rune
	historyIndex     int
	historyDraft     []rune
	lines            chan string
	errors           chan error
	escapes          chan struct{}
	reasoningToggles chan struct{}
	bytes            chan byte
	readDone         chan error
	stop             chan struct{}
	done             chan struct{}
	closeOnce        sync.Once
	rawState         *readline.State
	inputFD          int
	pending          []byte
	pasting          bool
	paste            []byte
}

type terminalInputEditorWriter struct {
	editor *terminalInputEditor
}

func newInteractiveTerminalInput(inputFile *os.File, output *terminal) (terminalInputSession, error) {
	rawState, err := readline.MakeRaw(int(inputFile.Fd()))
	if err != nil {
		return terminalInputSession{}, err
	}
	cancelableInput := readline.NewCancelableStdin(inputFile)
	editor := &terminalInputEditor{
		input:            cancelableInput,
		output:           output.out,
		prompt:           output.promptValue(),
		lines:            make(chan string, 8),
		errors:           make(chan error, 1),
		escapes:          make(chan struct{}, 1),
		reasoningToggles: make(chan struct{}, 1),
		bytes:            make(chan byte, 256),
		readDone:         make(chan error, 1),
		stop:             make(chan struct{}),
		done:             make(chan struct{}),
		rawState:         rawState,
		inputFD:          int(inputFile.Fd()),
		historyIndex:     0,
	}
	editor.mu.Lock()
	_, _ = io.WriteString(editor.output, "\x1b[?2004h\x1b[>1u")
	editor.renderInputLocked()
	editor.mu.Unlock()

	output.stream.attach(readline.GetScreenWidth)
	output.out = terminalInputEditorWriter{editor: editor}
	output.loading.attach(output.stream, output.out)
	go editor.readBytes()
	go editor.run()

	return terminalInputSession{
		lines:            editor.lines,
		errors:           editor.errors,
		escapes:          editor.escapes,
		reasoningToggles: editor.reasoningToggles,
		promptManaged:    true,
		close: func() {
			output.stopLoading()
			output.stream.detach()
			output.loading.detach(output.stream)
			editor.close()
		},
	}, nil
}

func (writer terminalInputEditorWriter) Write(value []byte) (int, error) {
	if writer.editor == nil {
		return len(value), nil
	}
	return writer.editor.writeAbove(value)
}

func (editor *terminalInputEditor) writeAbove(value []byte) (int, error) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	editor.eraseInputLocked()
	n, err := editor.output.Write(value)
	editor.renderInputLocked()
	return n, err
}

func (editor *terminalInputEditor) readBytes() {
	buffer := make([]byte, 256)
	for {
		count, err := editor.input.Read(buffer)
		for _, value := range buffer[:count] {
			select {
			case editor.bytes <- value:
			case <-editor.stop:
				return
			}
		}
		if err != nil {
			select {
			case editor.readDone <- err:
			case <-editor.stop:
			}
			return
		}
	}
}

func (editor *terminalInputEditor) run() {
	defer close(editor.done)
	defer close(editor.lines)
	defer close(editor.errors)
	var escapeTimer *time.Timer
	var escapeTimeout <-chan time.Time
	for {
		select {
		case <-editor.stop:
			if escapeTimer != nil {
				escapeTimer.Stop()
			}
			return
		case err := <-editor.readDone:
			if errors.Is(err, io.EOF) {
				err = nil
			}
			editor.errors <- err
			return
		case value := <-editor.bytes:
			editor.pending = append(editor.pending, value)
			editor.consumePending()
			if len(editor.pending) == 1 && editor.pending[0] == '\x1b' {
				if escapeTimer == nil {
					escapeTimer = time.NewTimer(terminalInputEscapeDelay)
				} else {
					escapeTimer.Reset(terminalInputEscapeDelay)
				}
				escapeTimeout = escapeTimer.C
			} else {
				escapeTimeout = nil
			}
		case <-escapeTimeout:
			escapeTimeout = nil
			if len(editor.pending) == 1 && editor.pending[0] == '\x1b' {
				editor.pending = nil
				editor.signal(editor.escapes)
			}
		}
	}
}

func (editor *terminalInputEditor) consumePending() {
	for len(editor.pending) != 0 {
		if editor.pasting {
			if !editor.consumePaste() {
				return
			}
			continue
		}
		if bytes.HasPrefix(editor.pending, terminalBracketedPasteStart) {
			if len(editor.pending) < len(terminalBracketedPasteStart) {
				return
			}
			editor.pending = editor.pending[len(terminalBracketedPasteStart):]
			editor.pasting = true
			editor.paste = nil
			continue
		}
		if terminalInputSequencePrefix(editor.pending) {
			return
		}
		if editor.consumeKnownSequence() {
			continue
		}
		if editor.pending[0] == '\x1b' {
			if len(editor.pending) == 1 {
				return
			}
			editor.pending = editor.pending[1:]
			editor.signal(editor.escapes)
			continue
		}
		if editor.consumeControl(editor.pending[0]) {
			editor.pending = editor.pending[1:]
			continue
		}
		value, size := utf8.DecodeRune(editor.pending)
		if value == utf8.RuneError && size == 1 && !utf8.FullRune(editor.pending) {
			return
		}
		editor.pending = editor.pending[size:]
		editor.insert(string(value))
	}
}

func (editor *terminalInputEditor) consumePaste() bool {
	if index := bytes.Index(editor.pending, terminalBracketedPasteEnd); index >= 0 {
		editor.paste = append(editor.paste, editor.pending[:index]...)
		editor.pending = editor.pending[index+len(terminalBracketedPasteEnd):]
		editor.pasting = false
		value := normalizeTerminalPaste(editor.paste)
		editor.paste = nil
		editor.insert(value)
		return true
	}
	keep := len(terminalBracketedPasteEnd) - 1
	if len(editor.pending) <= keep {
		return false
	}
	editor.paste = append(editor.paste, editor.pending[:len(editor.pending)-keep]...)
	editor.pending = editor.pending[len(editor.pending)-keep:]
	return false
}

func (editor *terminalInputEditor) consumeKnownSequence() bool {
	for _, key := range terminalShiftEnterKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.insert("\n")
			return true
		}
	}
	for _, key := range terminalSubmitKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.submit("")
			return true
		}
	}
	for _, key := range terminalBackspaceKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.backspace()
			return true
		}
	}
	for _, key := range terminalEOFKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			if len(editor.buffer) == 0 {
				select {
				case editor.errors <- nil:
				default:
				}
			}
			return true
		}
	}
	for _, key := range terminalEscapeKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.signal(editor.escapes)
			return true
		}
	}
	for _, key := range terminalControlOKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.signal(editor.reasoningToggles)
			return true
		}
	}
	for _, key := range terminalInterruptKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.submit(terminalInterruptInput)
			return true
		}
	}
	for _, key := range terminalLeftKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.moveCursor(-1)
			return true
		}
	}
	for _, key := range terminalRightKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.moveCursor(1)
			return true
		}
	}
	for _, key := range terminalUpKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.moveHistory(-1)
			return true
		}
	}
	for _, key := range terminalDownKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.moveHistory(1)
			return true
		}
	}
	for _, key := range terminalHomeKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.moveLineBoundary(false)
			return true
		}
	}
	for _, key := range terminalEndKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.moveLineBoundary(true)
			return true
		}
	}
	for _, key := range terminalDeleteKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			editor.deleteForward()
			return true
		}
	}
	for _, key := range terminalIgnoredKeys {
		if bytes.HasPrefix(editor.pending, key) {
			editor.pending = editor.pending[len(key):]
			return true
		}
	}
	return false
}

func (editor *terminalInputEditor) consumeControl(value byte) bool {
	switch value {
	case '\r', '\n':
		editor.submit("")
	case 3:
		editor.submit(terminalInterruptInput)
	case 4:
		if len(editor.buffer) == 0 {
			select {
			case editor.errors <- nil:
			default:
			}
		}
	case 8, 127:
		editor.backspace()
	case 15:
		editor.signal(editor.reasoningToggles)
	default:
		return value < 32
	}
	return true
}

func (editor *terminalInputEditor) insert(value string) {
	if value == "" {
		return
	}
	editor.mu.Lock()
	editor.eraseInputLocked()
	inserted := []rune(value)
	editor.buffer = append(editor.buffer, make([]rune, len(inserted))...)
	copy(editor.buffer[editor.cursor+len(inserted):], editor.buffer[editor.cursor:len(editor.buffer)-len(inserted)])
	copy(editor.buffer[editor.cursor:], inserted)
	editor.cursor += len(inserted)
	editor.renderInputLocked()
	editor.mu.Unlock()
}

func (editor *terminalInputEditor) backspace() {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if editor.cursor == 0 {
		return
	}
	editor.eraseInputLocked()
	copy(editor.buffer[editor.cursor-1:], editor.buffer[editor.cursor:])
	editor.buffer = editor.buffer[:len(editor.buffer)-1]
	editor.cursor--
	editor.renderInputLocked()
}

func (editor *terminalInputEditor) deleteForward() {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if editor.cursor >= len(editor.buffer) {
		return
	}
	editor.eraseInputLocked()
	copy(editor.buffer[editor.cursor:], editor.buffer[editor.cursor+1:])
	editor.buffer = editor.buffer[:len(editor.buffer)-1]
	editor.renderInputLocked()
}

func (editor *terminalInputEditor) moveCursor(delta int) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	next := editor.cursor + delta
	if next < 0 || next > len(editor.buffer) {
		return
	}
	editor.eraseInputLocked()
	editor.cursor = next
	editor.renderInputLocked()
}

func (editor *terminalInputEditor) moveLineBoundary(toEnd bool) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	next := editor.cursor
	if toEnd {
		for next < len(editor.buffer) && editor.buffer[next] != '\n' {
			next++
		}
	} else {
		for next > 0 && editor.buffer[next-1] != '\n' {
			next--
		}
	}
	if next == editor.cursor {
		return
	}
	editor.eraseInputLocked()
	editor.cursor = next
	editor.renderInputLocked()
}

func (editor *terminalInputEditor) moveHistory(delta int) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if len(editor.history) == 0 {
		return
	}
	if editor.historyIndex == len(editor.history) {
		editor.historyDraft = append([]rune(nil), editor.buffer...)
	}
	next := editor.historyIndex + delta
	if next < 0 || next > len(editor.history) || next == editor.historyIndex {
		return
	}
	editor.eraseInputLocked()
	editor.historyIndex = next
	if next == len(editor.history) {
		editor.buffer = append([]rune(nil), editor.historyDraft...)
	} else {
		editor.buffer = append([]rune(nil), editor.history[next]...)
	}
	editor.cursor = len(editor.buffer)
	editor.renderInputLocked()
}

func (editor *terminalInputEditor) submit(forced string) {
	editor.mu.Lock()
	editor.eraseInputLocked()
	value := string(editor.buffer)
	if forced != "" {
		value = forced
	}
	if forced == "" {
		_, _ = io.WriteString(editor.output, terminalInputDisplay(editor.prompt, editor.buffer)+"\n")
		if strings.TrimSpace(value) != "" {
			editor.history = append(editor.history, append([]rune(nil), editor.buffer...))
		}
	}
	editor.buffer = nil
	editor.cursor = 0
	editor.historyIndex = len(editor.history)
	editor.historyDraft = nil
	editor.renderInputLocked()
	editor.mu.Unlock()

	select {
	case editor.lines <- value:
	case <-editor.stop:
	}
}

func (editor *terminalInputEditor) signal(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (editor *terminalInputEditor) eraseInputLocked() {
	if editor.renderedRows == 0 {
		return
	}
	_, _ = io.WriteString(editor.output, "\r")
	if editor.cursorRow > 0 {
		_, _ = io.WriteString(editor.output, "\x1b["+strconv.Itoa(editor.cursorRow)+"A")
	}
	for row := 0; row < editor.renderedRows; row++ {
		_, _ = io.WriteString(editor.output, "\x1b[2K")
		if row+1 < editor.renderedRows {
			_, _ = io.WriteString(editor.output, "\x1b[1B\r")
		}
	}
	if editor.renderedRows > 1 {
		_, _ = io.WriteString(editor.output, "\x1b["+strconv.Itoa(editor.renderedRows-1)+"A\r")
	}
	editor.renderedRows = 0
	editor.cursorRow = 0
}

func (editor *terminalInputEditor) renderInputLocked() {
	display := terminalInputDisplay(editor.prompt, editor.buffer)
	_, _ = io.WriteString(editor.output, display)
	width := readline.GetScreenWidth()
	editor.renderedRows = terminalStreamRows(display, width)
	targetRow, targetColumn := terminalInputPosition(terminalInputDisplay(editor.prompt, editor.buffer[:editor.cursor]), width)
	editor.cursorRow = targetRow
	_, _ = io.WriteString(editor.output, "\r")
	if editor.renderedRows-1 > targetRow {
		_, _ = io.WriteString(editor.output, "\x1b["+strconv.Itoa(editor.renderedRows-1-targetRow)+"A")
	}
	if targetColumn > 0 {
		_, _ = io.WriteString(editor.output, "\x1b["+strconv.Itoa(targetColumn)+"C")
	}
}

func terminalInputDisplay(prompt string, buffer []rune) string {
	lines := strings.Split(string(buffer), "\n")
	var display strings.Builder
	display.WriteString(prompt)
	display.WriteString(lines[0])
	for _, line := range lines[1:] {
		display.WriteString("\n  ")
		display.WriteString(line)
	}
	return display.String()
}

func terminalInputPosition(value string, width int) (row, column int) {
	if width <= 0 {
		width = terminalStreamFallbackWidth
	}
	for _, current := range []rune(terminalANSIEscape.ReplaceAllString(value, "")) {
		if current == '\n' {
			row++
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
			row++
			column = 0
		}
		column += cellWidth
	}
	return row, column
}

func normalizeTerminalPaste(value []byte) string {
	result := strings.ReplaceAll(string(value), "\r\n", "\n")
	return strings.ReplaceAll(result, "\r", "\n")
}

func terminalInputSequencePrefix(value []byte) bool {
	for _, sequence := range terminalInputSequences {
		if len(value) < len(sequence) && bytes.HasPrefix(sequence, value) {
			return true
		}
	}
	return false
}

func (editor *terminalInputEditor) close() {
	editor.closeOnce.Do(func() {
		close(editor.stop)
		_ = editor.input.Close()
		<-editor.done
		editor.mu.Lock()
		editor.eraseInputLocked()
		_, _ = io.WriteString(editor.output, "\x1b[?2004l\x1b[<u")
		editor.mu.Unlock()
		_ = readline.Restore(editor.inputFD, editor.rawState)
	})
}
