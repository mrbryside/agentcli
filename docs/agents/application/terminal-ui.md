# Terminal UI

Read this when changing interactive input, live output, Markdown rendering, keyboard controls, loading state, or root/task-session view switching.

`Agent.RunTerminal` remains the public Go entry point; “Terminal UI” is the user-facing name. Interactive TTY input uses the prompt-aware editor in `terminal_input.go`, while non-TTY input keeps the line-scanner fallback. The editor owns raw-mode input, bracketed paste, multiline editing, cursor movement, prompt history, and redraws so asynchronous output cannot erase the text the user is typing.

The live renderer in `terminal_stream.go` appends provider fragments to Markdown source and rerenders the document with Glamour. It commits stable prefix lines to scrollback and redraws only a bounded mutable tail, which prevents duplicate output while keeping token updates smooth. Markdown headings suppress literal marker prefixes, inline code has no background block, and fenced code uses the One Dark theme. Root and task-session views use the same rendering rules and serialized output path.

Reasoning is stored separately from assistant content. It is collapsed by default, rendered dimly only when present, and toggled globally with `Ctrl+O`. A toggle rebuilds the selected view from stored messages plus safe visual events, retained notifications, and the active approval; alerts must not disappear during that redraw. Loading is a transient status indicator rather than synthetic reasoning or assistant text, and it must not restart while an approval is active.

Managed runtime logging from project config or `WithLogLevel` is captured while
an interactive Terminal is attached, so records do not interleave with the
prompt or streaming response.
`Ctrl+L` (or `/logs`) opens a modal view containing up to the latest 2,000
records and follows new records live; toggling back reconstructs the selected
root or task-session view while its run continues. Non-interactive terminal
runs and caller-supplied `WithLogger` handlers retain their existing output.

The Terminal keeps `/agents`, `/agent`, `/agent-status`, `/back`, and `/close`
as compatibility commands. User-facing output calls definitions “task agents”
and persisted instances “task sessions.” Task tool calls render `new` or
`resume` from `task_id`, and task tool results render the protocol state
`running`, `completed`, `incomplete`, or `error`. Storage's empty subagent
lifecycle is rendered as `resumable`; storage `failed` is rendered as the
model-facing `error`.

Interactive root views render concurrent `task` calls as one animated row per
call ID. Each row shows the agent and task description while running, updates
independently when results arrive out of order, and is committed to scrollback
with its terminal task state after the whole task batch finishes. Other tool
calls retain the shared loading row, and non-interactive output retains the
ordinary tool call/result transcript.

The opening and redrawn root banner appends the context-window size reported by
the active model's `ModelMetadataProvider` to the configured model name, for
example `qwen3.6-35b · 120k context`. Missing or invalid metadata is visible as
`- context` rather than removing the field.

Interactive controls are:

- `Shift+Enter`: insert a newline without submitting.
- `Up` / `Down`: navigate prompts entered during the current Terminal UI process.
- `Esc`: interrupt the active root or task-session run without exiting.
- `Ctrl+O`: expand or collapse all reasoning.
- `Ctrl+L`: open or close the runtime-log view.
- `Ctrl+C`: arm exit; a second press within two seconds exits immediately.

Switching task-session views changes only the visible subscription and transcript. It does not cancel a running task agent. Background results and queued root messages continue to use their owning session, while only the active view may render live content.

View reconstruction uses the same `❯` user prompt and completed task-progress
rows as live rendering. It also preserves the live blank row between reasoning
and assistant content and between a completed response and the next user turn,
including after a `Ctrl+O` reasoning toggle. Stored tool-call assistant content
is rendered before the call, preserving transcript order. If the selected main
or task session still has an active turn, its stored turn messages are excluded
and that turn is rebuilt once from retained run events; this prevents duplicated
or reordered content after `/agent`, `/back`, `Ctrl+O`, or leaving the runtime-log
view.

Back to [application/index.md](index.md).
