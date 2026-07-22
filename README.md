# harness-api

`harness-api` is a small Go library for event-sourced model streaming and
provider-neutral agent turns. It keeps persisted conversations independent of
provider SDK types, while exposing retained event histories, live subscriptions,
and final results.

## Package layout

```
provider/                       generic streaming events, immutable state, and Stream
provider/openai/                go-openai provider adapter
storage/                        generic persisted message domain and MessageStorage
storage/inmemory/               concurrency-safe in-memory transcript storage
agentruntime/                   turns, events, pure State/Effects/Result, Runtime
agentruntime/modeladapter/openai/ generic-message to OpenAI model adapter
toolexecution/                  tool registry and channel-driven worker pool
permission/                     provider-neutral capability permission domain and policy
```

## Streaming provider

Use `provider.StartStream` with an implementation such as
`provider/openai.NewProvider`. A provider `Stream` replays its current
`StreamEvent` history through `Subscribe(ctx)` and folds that history through
`Result()`. These are provider-stream semantics; they are deliberately
different from `agentruntime.Run.Subscribe`, which is live-only.
The OpenAI provider accepts request-level tools, so each agent turn can supply
its own registry catalog.

```go
stream, err := provider.StartStream(ctx, openaiProvider, request)
if err != nil {
	return err // setup failure
}
for event := range stream.Subscribe(ctx) {
	// content, reasoning, tool calls, completion, or failure
}
result, err := stream.Result()
```

## Agent runtime

A **session** is a long-lived conversation transcript identified by a
caller-supplied `SessionID`. A **turn** is one `Runtime.Start` or
`Runtime.StartSubscribed` call and all of the model and tool rounds caused by
its user message. `TurnID` is generated when omitted, or callers may provide a
previously unused value.

`Runtime` permits turns in different sessions to run concurrently, but permits
only one active turn for a session. Starting another turn in that session
returns `agentruntime.ErrTurnInProgress`; a previously persisted turn ID
returns `agentruntime.ErrTurnExists`.

The Echo server adds a bounded per-session FIFO above this strict runtime
contract. An idle-session POST starts immediately; another POST for the same
active session returns `202 Accepted` with `status: "queued"` and a queue
position. Other sessions continue in parallel. Configure the waiting bound with
`agentcli.WithServerTurnQueueLimit`.

The caller creates and owns buffered shared channels:

- `ToolRequests`: runtime sends `ToolRequest` values; `Executor.Run` consumes them.
- `ToolResults`: executor sends correlated `ToolResultEnvelope` values; runtime is its sole consumer.
- `ToolInterrupts`: runtime sends exact-turn `ToolInterrupt` values; executor consumes them.
- `PermissionRequests`: executor sends caller-visible permission prompts; runtime records them as retained events.
- `PermissionDecisions`: callers approve through `Runtime.ResolvePermission` and the executor consumes the correlated decision.

The runtime never closes these channels. Start a registry-backed executor with
the same channels, pass `registry.Definitions()` to `Runtime`, and use the
OpenAI model adapter at the provider boundary:

```go
registry := toolexecution.NewRegistry()
if err := registry.Register(toolexecution.Tool{
	Definition: agentruntime.ToolDefinition{
		Name: "lookup_status", InputSchema: json.RawMessage(`{"type":"object"}`),
	},
	Handler: lookupStatus,
}); err != nil {
	return err
}

model := openaiadapter.New(
	provideropenai.NewProvider(provideropenai.Config{APIKey: apiKey}),
	openaiadapter.Config{Model: "gpt-4.1-mini"},
)
runtime, err := agentruntime.New(ctx, agentruntime.Config{
	Model: model, Messages: inmemory.NewMessageStorage(),
	Tools: registry.Definitions(),
	ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts,
})
```

## `agentcli` convenience API

`agentcli` owns the runtime, tool executor, private channels, and in-memory
stores for a straightforward application integration. `LoadProject` reads the
provider configuration, `.agentcli/MAIN.md`, `AGENTS.md`, and available skills.
Applications still register their own executable tools explicitly. `agentcli.WithCustomTool`
provides typed inputs and outputs, automatic object-schema inference, strict argument
decoding, automatic result encoding, and functional permission/confirmation options.
The lower-level `WithTool(toolexecution.Tool{...})` remains available for advanced raw
JSON handlers. `toolexecution` owns the
framework built-ins: the restricted `load_skill` capability and the root-only
subagent management tools. `agentcli` wires their project and lifecycle
dependencies when those capabilities are configured.

```go
project, err := agentcli.LoadProject(projectRoot)
if err != nil {
	return err
}
type lookupInput struct {
	Service string `json:"service" description:"Service to inspect" minLength:"1"`
}
type lookupOutput struct {
	Status string `json:"status"`
}

agent, err := agentcli.New(ctx,
	agentcli.WithProject(project),
	agentcli.WithCustomTool(
		"lookup_status",
		"Look up the current service status.",
		func(ctx context.Context, input lookupInput) (lookupOutput, error) {
			return lookupStatus(ctx, input.Service)
		},
		agentcli.StaticToolPermission(toolexecution.PermissionConfig{
			Actions: []permission.Action{permission.NetworkAccess},
			Risk:    permission.RiskMedium,
			Reason:  "Lookup calls an external status service.",
		}),
	),
)
if err != nil {
	return err
}
defer agent.Close()

run, subscription, err := agent.StartSubscribed(ctx, agentruntime.Request{
	SessionID: "example-session",
	Message: agentruntime.Message{
		Type: agentruntime.MessageTypeUser, Content: "Summarize README.md.",
	},
})
if err != nil {
	return err
}
for event := range subscription.Events {
	if event.Type == agentruntime.ProviderEventReceived {
		fmt.Print(event.ProviderEvent.Content)
	}
}
```

`StartSubscribed` installs the live subscription before `RunStarted` can be
published, so normal clients do not miss the first event. `ListMessages` is a
separate transcript API: it returns an independent snapshot of the persisted
conversation and remains available after `Agent.Close`.

```go
messages, err := agent.ListMessages(ctx, "example-session")
if err != nil {
	return err
}
for _, message := range messages {
	fmt.Printf("%s: %s\n", message.Type, message.Content)
}
```

### Project configuration and skills

The CLI reads `.agentcli/config.yaml`; it never loads `.env`. Copy the safe
template and reference a process environment variable rather than storing a
live key in the project:

```sh
cp .agentcli/config.example.yaml .agentcli/config.yaml
```

```yaml
permission_mode: default
providers:
  openai:
    url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    request_timeout: 2m
```

Provider names are arbitrary and currently identify OpenAI-compatible
endpoints. URL, API key, and timeout are grouped under that provider.
`${VARIABLE}` expansion is supported when a deployment injects secrets, but no
dotenv file is read. The example CLI's model-facing `glob` and `read` tools
omit and deny live `.agentcli/config.yaml`, environment files, credential
stores, and private keys; safe `.example`, `.sample`, and `.template` files
remain readable. These protections do not replace rotating a credential that
has already been exposed.

`.agentcli/MAIN.md` is the required main-agent definition. Its frontmatter
selects the provider, model, project-skill allowlist, and caller-registered
custom-tool allowlist. Omit `skills` or `tools` when that capability is not
allowed; explicit empty lists are rejected. The Markdown body supplies main-only
instructions and is placed inside the grouped framework system message:

The main agent is always loaded, so `name` and `description` are not valid
`MAIN.md` fields. Those discovery fields remain required for subagents only.

```markdown
---
provider: openai
model: gpt-4o-mini
skills:
  - source-review
tools:
  - read_file
  - search
---

# Main role

Coordinate the work, choose capabilities deliberately, and report a clear
outcome.
```

`AGENTS.md` is loaded verbatim as the second, user-owned system message. Skills live at
`.agentcli/skill/<name>/SKILL.md` and have exactly two frontmatter fields:

```markdown
---
name: testing-go
description: Runs and diagnoses Go tests. Use when Go tests are requested or failing.
---

# Testing Go

Run the narrowest relevant test first, then run `go test ./...`.
```

Skill discovery follows progressive disclosure: the grouped framework system
message contains only every skill's name and description. The model
answers availability, description, and recommendation questions directly from
that catalog without loading a skill. It calls the restricted `load_skill`
capability only when applying a skill to the task or when the user explicitly
requests its full instructions; then that skill's Markdown body is returned as
the latest tool result. A repeat
call for the same unchanged skill returns a lightweight `already_loaded` result
while its instructions are still recent. The runtime returns the full body as a
new tool result when it is at least 10 turns or approximately 12,000 tokens old,
or when its content hash changes. Skill files do not configure providers,
models, or application tools.

The refresh thresholds are configurable when constructing the agent. Setting a
threshold to zero disables that threshold:

```go
agent, err := agentcli.New(ctx,
	agentcli.WithProject(project),
	agentcli.WithSkillReloadPolicy(agentcli.SkillReloadPolicy{
		MaxTurnDistance:  10,
		MaxTokenDistance: 12_000,
	}),
)
```

See [`agentcli/example/README.md`](agentcli/example/README.md) for provider
setup, permission handling, custom tools, interruption, and concurrent
sessions. The full Docusaurus guide lives in [`documentation/`](documentation/)
and documents the CLI, every HTTP/SSE route, typed schema inference, safety,
skills, subagents, and complete applications. `modechanges` and `reconnect` use
local models and need no key:

```sh
go run ./agentcli/example/basic
go run ./agentcli/example/events
go run ./agentcli/example/permissions
go run ./agentcli/example/interrupt
go run ./agentcli/example/customtool
go run ./agentcli/example/parallelsessions
go run ./agentcli/example/modechanges
go run ./agentcli/example/reconnect
go run ./agentcli/example/server
```

### Subagent sessions

Projects can define asynchronous, one-level child sessions in
`.agentcli/agent/<name>/<name>.md`. The directory, filename, and frontmatter
`name` must match; `description`, `provider`, `model`, and a Markdown body are
required. `provider` references a configured provider name, so credentials
stay in `.agentcli/config.yaml`.

```markdown
---
name: researcher
description: Research technical topics and compare solutions.
provider: openai
model: gpt-4o-mini
skills:
  - source-review
tools:
  - read_file
  - search
---

Return evidence, trade-offs, and a concise recommendation.
```

`skills` is an optional allowlist of names from `.agentcli/skill`. `tools` is
an allowlist of caller tools registered with `agentcli.WithCustomTool` or the
advanced raw `agentcli.WithTool`. If either is
omitted, that subagent receives none of that capability; explicit empty lists
are rejected. Referencing
an unavailable skill fails `LoadProject`; referencing an unregistered tool
fails `agentcli.New`. Definitions appear in the root agent's static
`<available_subagents>` catalog.
Open child state is instead a trusted, ephemeral `<system-reminder>` rebuilt
for each parent provider round. Every instance receives a short random,
session-local display name, and the reminder maps that friendly name to its
tool ID alongside status, turn, unread, and queue counts. It never persists in
raw user messages or includes child answer content.

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithMaxSubagents(4),
    // agentcli.WithSubagentStorage(applicationStore),
)
```

The framework exposes exactly five static, root-only model tools:
`start_subagent`, `send_subagent_message`, `close_subagent`,
`list_subagents`, and `subagent_status`. `start_subagent` is always
asynchronous. With no open child it creates one; with exactly one it routes the
message to that child; with multiple children it returns their friendly names
and requires the parent to ask the user which one to continue. Explicit
new/separate/parallel intent sets `new_instance: true`. The parent continues
useful independent work or finishes its response. Completion is delivered
separately. `subagent_status` answers
done/running/progress questions from lifecycle metadata without loading or
observing the transcript. A child completion or failure emits a compact live
callback containing the outcome, terminal error, and final assistant answer
when one exists. The interactive terminal immediately starts a new parent
turn from that trusted runtime event, even if the child view is open. If no UI
is subscribed, durable unread metadata remains in the next parent reminder.
No blocking wait tool is exposed to the model.
Use `/agent-status <id-or-display-name>` for an immediate transcript-free status snapshot,
including while another parent response is running.
The parent answers simple questions and code-generation requests itself;
delegation is reserved for specialized investigation, meaningful context
isolation, parallel work, or an explicit user request. A parent that needs
more detail uses `send_subagent_message` with a focused follow-up, then works
or waits passively for its next callback instead of polling. After consuming
and delivering a bounded one-shot result, the parent closes that child unless
there is a concrete planned follow-up, queued work, unresolved work requiring
the same context, or explicit ongoing collaboration. A merely possible later
question does not keep it open. Children receive
only the tools and skills allowed by their definition and never receive any
subagent-management tools, so they cannot create nested children or manage
siblings. `Agent.ReadSubagent` remains a low-level application recovery API;
it is not a model tool and the terminal UI uses `ListMessages` plus
`SubagentRun` for child history and streaming.

Closed children retain their transcript and completed event history but reject
new messages. Interrupting a parent turn interrupts child work created by that
same tool chain; closing the root agent closes all of its children. Terminal
navigation uses `/agents`, `/agent <id-or-display-name>`, `/back`, and
`/close <id-or-display-name>`.

### HTTP API with Echo

`agentcli` can expose the same agent through an Echo-powered JSON and SSE API;
there is no separate `agentserver` package. The listener is local-only by
default (`127.0.0.1:8080`). Add authentication, CORS, logging, or rate limits
with an Echo middleware before making a non-local deployment reachable.

```go
agent, err := agentcli.New(ctx,
	agentcli.WithProject(project),
	// Register only caller-owned tools with WithCustomTool(...) or WithTool(...).
)
if err != nil {
	return err
}
defer agent.Close()

return agent.RunServer(
	agentcli.WithServerAddress("127.0.0.1:8080"),
	agentcli.WithServerTurnQueueLimit(64),
	// agentcli.WithServerMiddleware(yourEchoMiddleware),
)
```

`agent.RunServer` blocks until the agent closes, the listener stops, or it
fails. Use `agentcli.NewServer` when embedding the handler in an existing
process; `server.Echo()` provides the configured `*echo.Echo` for adding
application routes before `server.Run()`.

| Endpoint | Purpose |
| --- | --- |
| `GET /healthz` | Liveness response. |
| `POST /v1/sessions/{sessionID}/turns` | Starts immediately or enters the session FIFO; use `Accept: text/event-stream` to wait and stream. |
| `GET /v1/sessions/{sessionID}/events` | Streams retained/live activity across user turns and automatic subagent callback turns with one session cursor. |
| `GET /v1/sessions/{sessionID}/turns/{turnID}` | Reads queued position, active status, or terminal result. |
| `GET /v1/sessions/{sessionID}/turns/{turnID}/events` | Waits for queued admission, then streams retained/live events; supports `Last-Event-ID` or `?after=`. |
| `POST /v1/sessions/{sessionID}/turns/{turnID}/interrupt` | Interrupts an active turn or cancels queued work before execution. |
| `GET /v1/sessions/{sessionID}/messages` | Returns the session transcript. |
| `POST /v1/permissions/{permissionID}/decisions` | Resolves a pending permission. |
| `POST /v1/confirmations/{confirmationID}/decisions` | Answers a custom-tool confirmation with `yes` or `no`. |
| `GET` / `PUT /v1/permission-mode` | Reads or changes the agent-global mode. |

When a project has subagent definitions, nested child chat is scoped by its
owning parent session. Use `GET /v1/subagent-definitions`, then
`POST|GET /v1/sessions/{parentSessionID}/subagents`; each child supports
`GET|DELETE /v1/sessions/{parentSessionID}/subagents/{subagentID}`, `POST`
`.../turns`, `GET .../messages`, and the same `GET .../turns/{turnID}`,
`.../events`, and `POST .../interrupt` lifecycle endpoints as a root turn.
Nested SSE accepts `Last-Event-ID` or `?after=` and has the same retained/live
cursor fence as root SSE. Parent ownership is required for every nested route.
The server automatically continues completed child callbacks in the parent
session by default; disable this only with
`WithServerAutoContinueSubagents(false)` when the host owns callback delivery.

Example request and event stream:

```sh
curl -sS -X POST http://127.0.0.1:8080/v1/sessions/demo/turns \
  -H 'Content-Type: application/json' \
  -d '{"message":"Hello"}'
curl -NsS http://127.0.0.1:8080/v1/sessions/demo/turns/TURN_ID/events
```

Each SSE event includes a numeric `id`. Persist it, then reconnect with
`Last-Event-ID`; the server fences the retained replay before consuming live
events, so it does not create a replay/live gap. A permission prompt survives
an SSE disconnect: post its ID together with the event's session, turn, and
call IDs to `/v1/permissions/{permissionID}/decisions` with a decision such as
`allow_once` or `deny`.

## Permissions

Permissions are runtime/caller events, never a model-callable tool. Tools declare
capabilities (`filesystem.read`, `filesystem.write`, `process.execute`,
`network.access`, `sandbox.bypass`) and the executor applies a pure policy before
entering its worker pool. A request waiting for a person therefore does not use a
worker. Prompt, resolve, cancel, and permission-mode events are retained, so a
reconnecting UI can recover them and answer a request much later while the
process remains alive.

Modes are `default` (ask), `acceptEdits`, `criticalOnly`, `dontAsk`, `plan`, and
`unrestricted`. The outcome comes from each custom tool's declared actions and
risk: for example, a RiskMedium tool asks in `default`, is automatically allowed
in `criticalOnly` and `unrestricted`, and is denied in `dontAsk` and `plan`.
Explicit rules apply `deny > ask > allow > default` precedence, so an explicit
deny rule always wins. A denial is stored as a normal `ToolResultDenied`,
allowing the model to continue safely.

`toolexecution.StaticPermission` is the simple per-tool setting when every call
uses the same permission class. `acceptEdits` automatically permits only tools
whose declared actions are exclusively `filesystem.write`; it asks for other
actions. `criticalOnly` asks only for `RiskHigh` tools and permits `RiskLow` and
`RiskMedium`. Leaving both permission descriptors nil deliberately makes that
custom tool unguarded.

The mode can change while the agent is running:

```go
previous := agent.PermissionMode()
if err := agent.SetPermissionMode(ctx, permission.CriticalOnly); err != nil {
	return err
}
```

Every `RunStarted` event carries the current mode in
`event.PermissionMode.Current`. A real transition publishes
`PermissionModeChanged` to each active run with both `Previous` and `Current`;
setting the same mode is a no-op. Existing permission prompts remain pending,
while newly received tool requests use the new policy. If a queued or approved
call belongs to an older mode epoch, it fails safely and asks the model to retry
instead of inheriting broader access. The terminal supports
`/mode` to inspect the current value and `/mode MODE` to change it.

`Run.Subscribe(ctx)` is live-only. It returns an `EventSubscription` whose
`Cursor` fences the retained history from subsequent `Events`; it does not
replay the events through that cursor. Persist the last processed
`EventCursor` (each event exposes one through `event.Cursor()`) with the
session/turn view. `Run.Result()` is available after the terminal event.
Events receive per-turn sequence numbers. The public state flow is functional:
`State` appends an immutable event log, `Effects` derives side effects without
performing them, and `Result` folds a terminal history into `RunResult`.

To reconnect without a gap, install the new live subscription first, then
recover the fenced range, then consume the live queue. The subscription keeps
events committed after its cursor queued while the retained range is rendered:

```go
// lastCursor is the last event durably rendered for this exact run. Use
// agentruntime.EventCursor{} when no event has been persisted yet.
subscription := run.Subscribe(ctx)
backfill, err := run.EventsBetween(lastCursor, subscription.Cursor)
if err != nil {
	return err
}
for _, event := range backfill {
	render(event)
	lastCursor = event.Cursor()
}
for event := range subscription.Events {
	render(event)
	lastCursor = event.Cursor() // persist this cursor with the rendered event
}
```

Tools are requested in provider order. The runtime waits for every result in a
round, persists result messages in that original order, then starts the next
model round. Tool handlers may complete in parallel; sessions remain isolated.

## Tool confirmations

A custom tool may require a separate Yes/No confirmation that presents
information to the user before execution. This is not authorization:
permission modes, grants, and `unrestricted` never bypass it. The descriptor is
evaluated from that invocation's arguments, and the handler does not occupy a
worker while waiting.

```go
tool := toolexecution.Tool{
    Definition: agentruntime.ToolDefinition{
        Name: "publish_report",
        InputSchema: json.RawMessage(`{
            "type":"object",
            "properties":{"destination":{"type":"string"}},
            "required":["destination"]
        }`),
    },
    Confirmation: func(arguments json.RawMessage) (confirmation.Description, error) {
        var input struct {
            Destination string `json:"destination"`
        }
        if err := json.Unmarshal(arguments, &input); err != nil {
            return confirmation.Description{}, err
        }
        return confirmation.Description{
            Title:   "Publish report",
            Message: "Publish this report now?",
            Details: "Destination: " + input.Destination,
        }, nil
    },
    Handler: publishReport,
}
```

`AgentConfirmationRequested` carries the title, message, details, IDs, and
optional expiry. Resolve it with `agent.ResolveConfirmation(ctx,
confirmation.Decision{..., Answer: confirmation.Yes})` or `confirmation.No`.
Yes executes the handler; No produces `ToolResultDeclined` and lets the model
continue. A late answer remains correlated by confirmation, session, turn, and
call IDs. Interruption cancels it. Non-interactive mode declines rather than
bypassing it. If a tool declares both permission and confirmation, permission
admission happens first and confirmation happens immediately before execution.

The terminal displays only `Yes` and `No`; answer with `y`/`n`, `/confirm ID`,
or `/decline ID`. `/confirmations` lists outstanding requests. HTTP clients use
`POST /v1/confirmations/{confirmationID}/decisions` with the correlated
session, turn, call, and `answer: "yes"|"no"`. Child-agent confirmations use
the equivalent endpoint nested under the owned subagent path.

## Interruption

Interrupt an exact active turn without affecting other sessions:

```go
err := runtime.Interrupt(ctx, sessionID, turnID, "user cancelled")
// or: err := run.Interrupt(ctx, "user cancelled")
```

Interruption is idempotent while the run remains active. It cancels the
provider stream, sends a turn-scoped `ToolInterrupt` for pending calls, stores
synthetic interrupted results as needed, and makes `Run.Result()` return
`agentruntime.ErrRunInterrupted`.

## Runnable example

[`agentruntime/example_test.go`](agentruntime/example_test.go) shows the full
channel wiring: caller-owned buffered channels, registry, executor, in-memory
storage, OpenAI adapter construction, `Runtime.StartSubscribed`, event subscription,
generated TurnID access, final result access, and the interruption API. It
uses a local completed model stream, so it needs no API key or network access:

```sh
go test ./agentruntime -run ExampleRuntime
```

## Terminal client

The reference terminal is owned by `agentcli`, so any constructed Agent can be
used as a manual playground before the application continues:

```go
if err := agent.RunTerminal(
	agentcli.WithTerminalSessionID("manual-check"),
); err != nil {
	return err
}

// The terminal exited; the Agent and its stored session remain available.
messages, err := agent.ListMessages(ctx, "manual-check")
```

`WithTerminalInput` and `WithTerminalOutput` support embedding and scripted
tests. `WithTerminalInitialPrompt` performs a one-shot run. `RunTerminal`
blocks, but `/exit` does not close the Agent, so callers may start direct turns
or `RunServer` afterward.

The `playground/terminal` executable is intentionally only a thin example. It
loads the project, registers its application-owned `glob`, `read`, and
`confirm_demo` tools, and calls `agent.RunTerminal`:

```sh
cp .agentcli/config.example.yaml .agentcli/config.yaml
# Edit .agentcli/config.yaml with the provider connection and root agent settings.
go run ./playground/terminal
```

Use `/new` for a fresh session, `/session` to inspect its ID, `/skills` to see
the metadata available for automatic model selection, `/clear` to redraw,
`/allow PERMISSION_ID`, `/allow-session PERMISSION_ID`, `/allow-project PERMISSION_ID`,
or `/deny PERMISSION_ID` to answer a pending request; use `/permissions` to list
late prompts; and
`/exit` to quit. Press Ctrl+C while a response is active to interrupt only
that turn. Tool execution is available only when the application registers a
typed custom tool or an advanced raw `toolexecution.Tool`; each tool owns its
semantic validation, handler, and permission description. `WithProjectRoot` identifies the project for
`AllowProject` permission grants; it does not register or scope tools.

Command-line arguments retain one-shot behavior:

```sh
go run ./playground/terminal "Explain this repository"
```

Set `permission_mode: unrestricted` in `.agentcli/config.yaml` only after
consciously accepting the permissions declared by the application's custom
tools; the terminal prints a red warning. Other modes apply their configured
permission policy.

## Design guarantees

- Stored messages use the generic `storage.Message` domain, never provider SDK types.
- The grouped framework prompt (including `.agentcli/MAIN.md` and capability
  catalogs) and the separate `AGENTS.md` prompt are not written into the
  session transcript; a selected skill is retained through
  its ordinary `load_skill` tool-call/result round. Recent duplicate loads do
  not repeat the instruction body; stale or changed skills are refreshed as a
  new latest tool result.
- State, events, tool transports, storage reads, and result values are defensively copied.
- Run subscriptions are live-only, retained ranges are fetched explicitly with
  `EventsBetween`, and slow subscribers do not block a run or each other.
- Infrastructure, storage, transformation, routing, and provider failures end a run; a tool handler failure is a result sent back to the model.
