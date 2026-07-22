# Terminal UI

Read this when changing interactive input, live output, Markdown rendering, keyboard controls, loading state, or root/subagent view switching.

`Agent.RunTerminal` remains the public Go entry point; “Terminal UI” is the user-facing name. Interactive TTY input uses the prompt-aware editor in `terminal_input.go`, while non-TTY input keeps the line-scanner fallback. The editor owns raw-mode input, bracketed paste, multiline editing, cursor movement, prompt history, and redraws so asynchronous output cannot erase the text the user is typing.

The live renderer in `terminal_stream.go` appends provider fragments to Markdown source and rerenders the document with Glamour. It commits stable prefix lines to scrollback and redraws only a bounded mutable tail, which prevents duplicate output while keeping token updates smooth. Markdown headings suppress literal marker prefixes, inline code has no background block, and fenced code uses the One Dark theme. Root and subagent views use the same rendering rules and serialized output path.

Reasoning is stored separately from assistant content. It is collapsed by default, rendered dimly only when present, and toggled globally with `Ctrl+O`. Loading is a transient status indicator rather than synthetic reasoning or assistant text.

Interactive controls are:

- `Shift+Enter`: insert a newline without submitting.
- `Up` / `Down`: navigate prompts entered during the current Terminal UI process.
- `Esc`: interrupt the active root or subagent run without exiting.
- `Ctrl+O`: expand or collapse all reasoning.
- `Ctrl+C`: arm exit; a second press within two seconds exits immediately.

Switching child views changes only the visible subscription and transcript. It does not cancel a running child. Background callbacks and queued root messages continue to use their owning session, while only the active view may render live content.

Back to [application/index.md](index.md).
