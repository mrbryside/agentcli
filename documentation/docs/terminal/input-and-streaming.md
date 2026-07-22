---
title: Input and streaming
sidebar_position: 2
---

# Input and streaming

The interactive terminal uses a prompt-aware editor. Assistant output,
loading state, reasoning, and the text currently being entered are separate
state, so streamed output can update without deleting the draft.

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| `Enter` | Send the complete draft as one user message. |
| `Shift+Enter` | Insert a newline without sending. |
| `Up` | Recall the previous prompt entered during this terminal process. |
| `Down` | Move toward newer history and restore the unsent draft. |
| `Left` / `Right` | Move the cursor within the draft. |
| `Home` / `End` | Move to the start or end of the current draft line. |
| `Backspace` / `Delete` | Remove text before or after the cursor. |
| `Ctrl+O` | Expand or collapse all provider reasoning in the active view. |
| `Esc` | Interrupt the active root or subagent response. |
| `Ctrl+C` twice | Exit the terminal. The second press must occur within two seconds. |

The editor accepts bracketed paste. Pasting several lines inserts one
multi-line draft and does not create several queued turns. Press `Enter` once
to send the pasted content.

Prompt history is held for the lifetime of the terminal process. Multi-line
prompts retain their line breaks. The same history is available while viewing
the root session or a child session; it is not persisted as terminal-editor
state across process restarts. Conversation messages remain available through
message storage independently.

## Enter a multi-line prompt

Type the first line, press `Shift+Enter`, and continue:

```text
❯ Compare these approaches:
  1. A worker pool
  2. One goroutine per task
```

Only the first line has the `❯` marker. Pressing `Enter` sends the complete
text as one turn.

## Streaming output

The spinner means that the current view is active and waiting for more work.
It is not model reasoning and is not included in the transcript.

Provider content fragments are appended to one assistant-content state. The
terminal re-renders that state as Markdown and updates the live tail while
leaving stable lines in terminal scrollback. This supports headings, lists,
emphasis, links, inline code, and fenced code blocks without printing each
partial fragment as a new line.

## Provider reasoning

Reasoning appears only when the selected provider sends a reasoning event. It
is separate from assistant content and initially appears collapsed:

```text
> thinking
```

Press `Ctrl+O` to expand every reasoning block in the active view. Expanded
reasoning is intentionally dimmer than the answer:

```text
⌄ thinking
  inspect the available records
  compare the possible outcomes
```

Press `Ctrl+O` again to collapse it. Root and subagent views use the same
behavior, and stored reasoning can be restored when a view is reopened.

## Send input while a response is active

Input remains editable while the current root or child turn is streaming. A
new root prompt is placed in the root queue and starts after the active turn
and higher-priority subagent callbacks are handled. Input entered in a child
view is sent to that child; if it is already running, the message is queued in
the child's mailbox.

Use `/session` to see whether the selected view is `active` or `idle`.
