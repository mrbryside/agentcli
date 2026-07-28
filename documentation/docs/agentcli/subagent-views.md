---
title: Subagent views
sidebar_position: 3
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Subagent views

An `agentcli` application can present every subagent as an independent subagent
view. A subagent view has its own session, transcript, active turn, stream cursor,
loading state, pending decisions, and queued messages. Switching views changes
what is visible; it must not stop work running in another view.

This application pattern applies to web, mobile, desktop, and custom
interactive clients using either the Go API or the HTTP API.

After the shared state model, choose the integration tab that matches the host
application. Both versions preserve the same session, turn, message, and event
semantics.

## State model

Keep normalized state instead of one shared output buffer:

```ts
type ViewID = "main-agent" | `subagent:${string}`;

type SubagentView = {
  subagentID: string;
  subagentSessionID: string;
  displayName: string;
  status?: "running" | "closed"; // omitted while retained and resumable
  currentSubagentTurnID?: string;
  messages: Message[];
  cursors: Record<string, number>; // keyed by subagent turn ID
  streamingSubagentTurnID?: string;
  queuedMessages: number;
};

type AgentUIState = {
  mainAgentSessionID: string;
  activeView: ViewID;
  mainAgentMessages: Message[];
  subagents: Record<string, SubagentView>;
};
```

Render `mainAgentMessages` only when `activeView === "main-agent"`. For a subagent view,
render only `subagents[subagentID].messages`. Provider events may continue to
update an inactive view's state without writing into the currently visible
view.

## Integration

<Tabs
  groupId="subagent-view-integration"
  defaultValue="agentcli"
  queryString="integration"
  values={[
    {label: 'AgentCLI Go', value: 'agentcli'},
    {label: 'HTTP API', value: 'http'},
  ]}>

<TabItem value="http">

### HTTP API

Use this version when the client connects to `Agent.RunServer`. JSON endpoints
provide snapshots and commands, while retained SSE streams provide live subagent
turn events.

### Discover subagent views

Read the definitions available for new subagents:

```text
GET /v1/subagent-definitions
```

Read instances owned by a main agent conversation:

```text
GET /v1/sessions/{mainAgentSessionID}/subagents?include_closed=true
```

Use `id` as the durable application key and `display_name` as the human-facing
label. Definition names are not instance identities because several subagents
may use the same definition.

Important response fields are:

| Field | UI use |
| --- | --- |
| `id` | Subagent view identity and nested-route key. |
| `subagent_session_id` | Provider-neutral subagent transcript identity. |
| `display_name` | Friendly tab/window label. |
| `status` | `running`, `closed`, or omitted while retained and resumable. |
| `current_subagent_turn_id` | Active subagent turn to attach to. |
| `last_subagent_turn_id` | Most recently completed subagent turn. |
| `last_result_error` | Failure summary for the completed turn. |
| `queued_messages` | Follow-ups waiting behind the active subagent turn. |
| `version` | Monotonic subagent metadata version. |

### Open a subagent view

Opening a subagent is a read operation. It does not start a model turn and does
not consume the main agent's result cursor.

1. Read the latest subagent record.
2. Fetch its complete provider-neutral transcript.
3. Replace the subagent view's message snapshot.
4. If it is `running`, attach to `current_subagent_turn_id`.
5. Select the subagent as the active view.

```js
async function openSubagent(mainAgentSessionID, subagentID) {
  const base =
    `/v1/sessions/${encodeURIComponent(mainAgentSessionID)}` +
    `/subagents/${encodeURIComponent(subagentID)}`;

  const [record, history] = await Promise.all([
    getJSON(base),
    getJSON(`${base}/messages`),
  ]);

  subagentStore.replace(record.id, {
    ...record,
    messages: history.messages,
  });

  if (record.status === "running" && record.current_subagent_turn_id) {
    resumeSubagentTurn(mainAgentSessionID, record.id, record.current_subagent_turn_id);
  }

  viewStore.select(`subagent:${record.id}`);
}
```

Do not append the history blindly every time a view opens. Replace it or merge
by message `id`; otherwise switching away and back duplicates messages.

### Resume an active subagent turn

Subagent turn streams retain events and use a numeric cursor scoped to that one
turn:

```text
GET /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns/{turnID}/events
```

Store a separate cursor for every `{subagentID, turnID}` pair. Reconnect with
`Last-Event-ID` or `?after=`.

```js
function resumeSubagentTurn(mainAgentSessionID, subagentID, turnID) {
  const subagent = subagentStore.get(subagentID);
  if (subagent.streamingSubagentTurnID === turnID) return;

  stopSubagentStream(subagentID);
  const after = subagent.cursors[turnID] || 0;
  const path =
    `/v1/sessions/${encodeURIComponent(mainAgentSessionID)}` +
    `/subagents/${encodeURIComponent(subagentID)}` +
    `/turns/${encodeURIComponent(turnID)}/events`;
  const url = after ? `${path}?after=${after}` : path;
  const source = new EventSource(url);

  subagentStreams.set(subagentID, source);
  subagentStore.patch(subagentID, {streamingSubagentTurnID: turnID});

  for (const name of runtimeEventNames) {
    source.addEventListener(name, (message) => {
      const event = JSON.parse(message.data);
      applySubagentEvent(subagentID, event);
      subagentStore.setCursor(subagentID, turnID, event.sequence);

      if (["run_completed", "run_failed", "agent_interrupted"].includes(event.type)) {
        source.close();
        subagentStreams.delete(subagentID);
        subagentStore.patch(subagentID, {streamingSubagentTurnID: undefined});
        refreshSubagentAfterTurn(mainAgentSessionID, subagentID, turnID);
      }
    });
  }
}
```

Persist the cursor only after applying the event. Event application should be
idempotent: messages are keyed by `id`, tool calls by `call_id`, and pending
permissions/confirmations by their request ID.

An SSE disconnect does not change subagent status. Reconnect from the last applied
cursor. Do not mark the subagent failed unless a retained `run_failed` event says
so.

### Continue queued subagent work

Sending a message to a retained subagent starts a new turn. Sending while it is
running queues the message behind the current turn:

```text
POST /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns
Content-Type: application/json

{"message":"Now compare the second option"}
```

The response contains the latest subagent record. If it has a new
`current_subagent_turn_id`, attach immediately. If `queued_messages` increased, keep the
current stream and show the queued state.

After a run-ending event, refresh the subagent record. The manager may already have
started the next queued message under a new turn ID:

```js
async function refreshSubagentAfterTurn(mainAgentSessionID, subagentID, completedTurnID) {
  const path =
    `/v1/sessions/${encodeURIComponent(mainAgentSessionID)}` +
    `/subagents/${encodeURIComponent(subagentID)}`;
  const record = await getJSON(path);
  subagentStore.patch(subagentID, record);

  if (
    record.status === "running" &&
    record.current_subagent_turn_id &&
    record.current_subagent_turn_id !== completedTurnID
  ) {
    resumeSubagentTurn(mainAgentSessionID, subagentID, record.current_subagent_turn_id);
  }
}
```

Also refresh the subagent record when the main agent session stream reports a
`subagent_result` for that subagent. This covers subagent work initiated by the
main agent model rather than directly by the UI.

### Switch views without interrupting

Changing `activeView` must not close the subagent's EventSource or cancel its
turn. Continue processing events into the subagent store in the background.

```js
function showMainAgent() {
  viewStore.select("main-agent");
}

function showSubagent(subagentID) {
  viewStore.select(`subagent:${subagentID}`);
}
```

Derive the loading indicator per view:

- main-agent loading comes from its admitted turn;
- subagent loading comes from `status === "running"` and `current_subagent_turn_id`;
- switching views only changes which loading indicator is visible;
- a background completion may update badges or notifications, but must not
  inject the subagent's text into the main-agent transcript.

### Permissions and confirmations

Subagent streams emit the same permission and confirmation events as main-agent turns.
Route answers through the ownership-scoped endpoints:

```text
POST /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/permissions/{permissionID}/decisions
POST /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/confirmations/{confirmationID}/decisions
```

Keep pending prompts associated with the subagent view even while another view is
active. A global notification may bring the user to the correct subagent, but the
decision IDs and session/turn/call correlation must remain unchanged.

### Close a subagent

```text
DELETE /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}
```

A retained incomplete, completed, or failed task can receive another message
using its exact ID. A running subagent accepts queued input.

HTTP `DELETE` is an explicit destructive command, not routine cleanup. It can
interrupt a running subagent, removes queued messages, and closes incomplete work.
Expose it only from a direct user action. It does not delete the transcript or
completed event history. Mark a successfully closed view read-only and keep it
available when `include_closed=true` is requested.

### Restore views after application reload

Messages alone are not a complete subagent-state snapshot. In particular,
`subagent_closed` is a system event and is not appended to the conversation
transcript. A page that reloads only messages can therefore lose the visible
close notification even though the durable subagent record remains closed.

1. Restore the main agent session ID.
2. List subagents with `include_closed=true`.
3. Recreate one view record per subagent ID.
4. Fetch messages only for views being shown, or eagerly when the subagent count
   is small.
5. For every `running` subagent, attach to `current_subagent_turn_id` with its saved cursor.
6. Reconnect the main agent session stream for automatic result turns.
7. When a result references a subagent, refresh that subagent record and transcript.

Treat the result of `include_closed=true` as the authoritative initial state.
Use `subagent_closed` only to update an already-hydrated page in real time.
This also makes reloads safe after a server restart, because subagent records come
from `SubagentStorage` while the server's session-event replay history is
process-local memory.

If cursors are not durable, fetch the transcript first and replay a subagent's
active turn from sequence zero. Merge messages and event-derived state by
identity so replay cannot duplicate visible content.

</TabItem>

<TabItem value="agentcli">

### AgentCLI Go package

Use this version when the UI and `Agent` run in the same Go process. It uses
the same provider-neutral subagent records, messages, and runtime events as the
HTTP version, without JSON or SSE serialization.

### Discover and restore subagent views

Definitions describe which subagent types may be started. Subagent records describe
the instances already owned by the main agent session:

```go
definitions := agent.SubagentDefinitions()

subagents, err := agent.ListSubagents(ctx, mainAgentSessionID, true)
if err != nil {
    return err
}

for _, subagent := range subagents {
    subagentStore.Replace(subagent.ID, subagent)
}
```

Use `storage.Subagent.ID` as the view key, `DisplayName` as its label, and
`SubagentSessionID` to read its transcript. As with the HTTP version, switching the
active view changes rendering only; it must not interrupt a running subagent.

### Open a subagent and resume streaming

Read the transcript with `ListMessages`. This is the UI-safe read path: it does
not mark the subagent's final answer as observed by the main agent model.

```go
messages, err := agent.ListMessages(ctx, subagent.SubagentSessionID)
if err != nil {
    return err
}
subagentStore.ReplaceMessages(subagent.ID, messages)

if subagent.Status != storage.SubagentStatusRunning || subagent.CurrentSubagentTurnID == "" {
    return nil
}

run, err := agent.SubagentRun(
    ctx,
    mainAgentSessionID,
    subagent.ID,
    subagent.CurrentSubagentTurnID,
)
if err != nil {
    return err
}

subscription := run.Subscribe(ctx)
retained, err := run.EventsBetween(
    agentruntime.EventCursor{},
    subscription.Cursor,
)
if err != nil {
    return err
}
for _, event := range retained {
    subagentStore.ApplyEvent(subagent.ID, event)
}
for event := range subscription.Events {
    subagentStore.ApplyEvent(subagent.ID, event)
}
```

Subscribe before reading retained events, then use the subscription cursor as
the replay fence. This prevents an event from being missed between history
loading and live delivery.

Run the subscription loop in its own goroutine. Keep one cancel function per
`{subagentID, turnID}` and cancel only when that turn ends, the subagent closes,
or the application shuts down—not when the user switches views.

### Start or continue subagent work

Start a project-defined subagent asynchronously:

```go
subagent, err := agent.StartSubagent(
    ctx,
    mainAgentSessionID,
    mainAgentTurnID,
    "researcher",                  // definition name
    "Compare the storage options", // initial message
    "storage research",            // optional label
)
if err != nil {
    return err
}
subagentStore.Replace(subagent.ID, subagent)
```

Continue an existing subagent with the same method whether it is retained or running:

```go
subagent, err = agent.SendSubagentMessage(
    ctx,
    mainAgentSessionID,
    subagent.ID,
    "Also evaluate migration cost",
)
if err != nil {
    return err
}
subagentStore.Replace(subagent.ID, subagent)
```

A retained subagent starts a new turn immediately. A running subagent queues the message
and returns an updated record with another entry in `Pending`. When the current
turn ends, refresh with `ListSubagents`; if `CurrentSubagentTurnID` changed, attach to
the new run using the same subscribe-then-replay sequence.

### Handle results and lifecycle changes

Agent owns model-created task delivery. Applications may observe
`SystemTaskCompleted` to refresh child-session views or consume validated
application metadata; they do not inject the result or create another main
turn. A host-created subagent turn already exposes its own retained/live run
events, so refresh the record after `RunCompleted`.

```go
for event := range agent.SubscribeSystemEvents(ctx) {
    if event.MainAgentSessionID != mainAgentSessionID {
        continue
    }
    switch event.Type {
    case agentcli.SystemTaskCompleted:
        subagents, err := agent.ListSubagents(ctx, mainAgentSessionID, true)
        if err != nil {
            reportError(err)
            continue
        }
        subagentStore.Merge(subagents)
    case agentcli.SystemSubagentClosed:
        if event.SubagentClosed != nil {
            subagent := event.SubagentClosed.Subagent
            subagentStore.Replace(subagent.ID, subagent)
        }
    }
}
```

Do not use `ReadSubagent` merely to render a subagent transcript. That method is
for advancing the main agent's durable observation cursor. Subagent-view rendering
should use `ListMessages`.

### Interrupt or close a subagent

Interrupt only the active turn while retaining the subagent for later messages:

```go
err := agent.InterruptSubagent(
    ctx,
    mainAgentSessionID,
    subagent.ID,
    "stopped by user",
)
```

For an explicit user-directed destructive close:

```go
closed, err := agent.CloseSubagent(ctx, mainAgentSessionID, subagent.ID)
if err != nil {
    return err
}
subagentStore.Replace(closed.ID, closed)
```

`CloseSubagent` interrupts active work when necessary, removes queued subagent
messages, cancels outstanding unreserved result obligations, and rejects
future sends. It still retains the subagent transcript and completed event history
for a read-only view. Releasing the obligations clears the result barrier;
final delivery still occurs only when an active main agent turn reaches its
completion boundary. Close does not create that turn. See
[Subagent lifecycle control](../capabilities/subagent-lifecycle-control.md).
Do not expose it as an automatic cleanup path; bind it to an explicit user
action. Normal completion and response-scope completion retain the task.

Closing retains the transcript. Keep the view available as read-only when the
application lists subagents with `includeClosed` set to `true`.

</TabItem>

</Tabs>
