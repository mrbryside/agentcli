---
title: Child views
sidebar_position: 3
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Child views

An `agentcli` application can present every subagent as an independent child
view. A child view has its own session, transcript, active turn, stream cursor,
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
type ViewID = "root" | `child:${string}`;

type ChildView = {
  subagentID: string;
  sessionID: string;
  displayName: string;
  status: "running" | "idle" | "closed";
  currentTurnID?: string;
  messages: Message[];
  cursors: Record<string, number>; // keyed by child turn ID
  streamingTurnID?: string;
  queuedMessages: number;
};

type AgentUIState = {
  parentSessionID: string;
  activeView: ViewID;
  rootMessages: Message[];
  children: Record<string, ChildView>;
};
```

Render `rootMessages` only when `activeView === "root"`. For a child view,
render only `children[subagentID].messages`. Provider events may continue to
update an inactive view's state without writing into the currently visible
view.

## Integration

<Tabs
  groupId="child-view-integration"
  defaultValue="agentcli"
  queryString="integration"
  values={[
    {label: 'AgentCLI Go', value: 'agentcli'},
    {label: 'HTTP API', value: 'http'},
  ]}>

<TabItem value="http">

### HTTP API

Use this version when the client connects to `Agent.RunServer`. JSON endpoints
provide snapshots and commands, while retained SSE streams provide live child
turn events.

### Discover child views

Read the definitions available for new children:

```text
GET /v1/subagent-definitions
```

Read instances owned by a parent conversation:

```text
GET /v1/sessions/{parentSessionID}/subagents?include_closed=true
```

Use `id` as the durable application key and `display_name` as the human-facing
label. Definition names are not instance identities because several children
may use the same definition.

Important response fields are:

| Field | UI use |
| --- | --- |
| `id` | Child view identity and nested-route key. |
| `session_id` | Provider-neutral child transcript identity. |
| `display_name` | Friendly tab/window label. |
| `status` | `running`, `idle`, or `closed`. |
| `current_turn_id` | Active child turn to attach to. |
| `last_turn_id` | Most recently completed child turn. |
| `last_turn_error` | Failure summary for the completed turn. |
| `queued_messages` | Follow-ups waiting behind the active child turn. |
| `version` | Monotonic child metadata version. |

### Open a child view

Opening a child is a read operation. It does not start a model turn and does
not consume the parent's callback cursor.

1. Read the latest child record.
2. Fetch its complete provider-neutral transcript.
3. Replace the child view's message snapshot.
4. If it is `running`, attach to `current_turn_id`.
5. Select the child as the active view.

```js
async function openChild(parentSessionID, subagentID) {
  const base =
    `/v1/sessions/${encodeURIComponent(parentSessionID)}` +
    `/subagents/${encodeURIComponent(subagentID)}`;

  const [record, history] = await Promise.all([
    getJSON(base),
    getJSON(`${base}/messages`),
  ]);

  childStore.replace(record.id, {
    ...record,
    messages: history.messages,
  });

  if (record.status === "running" && record.current_turn_id) {
    resumeChildTurn(parentSessionID, record.id, record.current_turn_id);
  }

  viewStore.select(`child:${record.id}`);
}
```

Do not append the history blindly every time a view opens. Replace it or merge
by message `id`; otherwise switching away and back duplicates messages.

### Resume an active child turn

Child turn streams retain events and use a numeric cursor scoped to that one
turn:

```text
GET /v1/sessions/{parentSessionID}/subagents/{subagentID}/turns/{turnID}/events
```

Store a separate cursor for every `{subagentID, turnID}` pair. Reconnect with
`Last-Event-ID` or `?after=`.

```js
function resumeChildTurn(parentSessionID, subagentID, turnID) {
  const child = childStore.get(subagentID);
  if (child.streamingTurnID === turnID) return;

  stopChildStream(subagentID);
  const after = child.cursors[turnID] || 0;
  const path =
    `/v1/sessions/${encodeURIComponent(parentSessionID)}` +
    `/subagents/${encodeURIComponent(subagentID)}` +
    `/turns/${encodeURIComponent(turnID)}/events`;
  const url = after ? `${path}?after=${after}` : path;
  const source = new EventSource(url);

  childStreams.set(subagentID, source);
  childStore.patch(subagentID, {streamingTurnID: turnID});

  for (const name of runtimeEventNames) {
    source.addEventListener(name, (message) => {
      const event = JSON.parse(message.data);
      applyChildEvent(subagentID, event);
      childStore.setCursor(subagentID, turnID, event.sequence);

      if (["run_completed", "run_failed", "agent_interrupted"].includes(event.type)) {
        source.close();
        childStreams.delete(subagentID);
        childStore.patch(subagentID, {streamingTurnID: undefined});
        refreshChildAfterTurn(parentSessionID, subagentID, turnID);
      }
    });
  }
}
```

Persist the cursor only after applying the event. Event application should be
idempotent: messages are keyed by `id`, tool calls by `call_id`, and pending
permissions/confirmations by their request ID.

An SSE disconnect does not change child status. Reconnect from the last applied
cursor. Do not mark the child failed unless a retained `run_failed` event says
so.

### Continue queued child work

Sending a message to an idle child starts a new turn. Sending while it is
running queues the message behind the current turn:

```text
POST /v1/sessions/{parentSessionID}/subagents/{subagentID}/turns
Content-Type: application/json

{"message":"Now compare the second option"}
```

The response contains the latest child record. If it has a new
`current_turn_id`, attach immediately. If `queued_messages` increased, keep the
current stream and show the queued state.

After a run-ending event, refresh the child record. The manager may already have
started the next queued message under a new turn ID:

```js
async function refreshChildAfterTurn(parentSessionID, subagentID, completedTurnID) {
  const path =
    `/v1/sessions/${encodeURIComponent(parentSessionID)}` +
    `/subagents/${encodeURIComponent(subagentID)}`;
  const record = await getJSON(path);
  childStore.patch(subagentID, record);

  if (
    record.status === "running" &&
    record.current_turn_id &&
    record.current_turn_id !== completedTurnID
  ) {
    resumeChildTurn(parentSessionID, subagentID, record.current_turn_id);
  }
}
```

Also refresh the child record when the parent session stream reports a
`subagent_callback` for that child. This covers child work initiated by the
parent model rather than directly by the UI.

### Switch views without interrupting

Changing `activeView` must not close the child's EventSource or cancel its
turn. Continue processing events into the child store in the background.

```js
function showRoot() {
  viewStore.select("root");
}

function showChild(subagentID) {
  viewStore.select(`child:${subagentID}`);
}
```

Derive the loading indicator per view:

- root loading comes from its admitted root turn;
- child loading comes from `status === "running"` and `current_turn_id`;
- switching views only changes which loading indicator is visible;
- a background completion may update badges or notifications, but must not
  inject the child's text into the root transcript.

### Permissions and confirmations

Child streams emit the same permission and confirmation events as root turns.
Route answers through the ownership-scoped endpoints:

```text
POST /v1/sessions/{parentSessionID}/subagents/{subagentID}/permissions/{permissionID}/decisions
POST /v1/sessions/{parentSessionID}/subagents/{subagentID}/confirmations/{confirmationID}/decisions
```

Keep pending prompts associated with the child view even while another view is
active. A global notification may bring the user to the correct child, but the
decision IDs and session/turn/call correlation must remain unchanged.

### Close a child

```text
DELETE /v1/sessions/{parentSessionID}/subagents/{subagentID}
```

Closing is cleanup for an idle child and rejects future messages. It does not
delete the transcript or completed event history. A running child returns
`409 conflict`; interrupt its active turn first, consume the terminal event and
callback, then close after the child becomes idle. Mark a successfully closed
view read-only and keep it available when `include_closed=true` is requested.

### Restore views after application reload

1. Restore the parent session ID.
2. List children with `include_closed=true`.
3. Recreate one view record per child ID.
4. Fetch messages only for views being shown, or eagerly when the child count
   is small.
5. For every `running` child, attach to `current_turn_id` with its saved cursor.
6. Reconnect the parent session stream for automatic callback turns.
7. When a callback references a child, refresh that child record and transcript.

If cursors are not durable, fetch the transcript first and replay a child's
active turn from sequence zero. Merge messages and event-derived state by
identity so replay cannot duplicate visible content.

</TabItem>

<TabItem value="agentcli">

### AgentCLI Go package

Use this version when the UI and `Agent` run in the same Go process. It uses
the same provider-neutral child records, messages, and runtime events as the
HTTP version, without JSON or SSE serialization.

### Discover and restore child views

Definitions describe which child types may be started. Child records describe
the instances already owned by the parent session:

```go
definitions := agent.SubagentDefinitions()

children, err := agent.ListSubagents(ctx, parentSessionID, true)
if err != nil {
    return err
}

for _, child := range children {
    childStore.Replace(child.ID, child)
}
```

Use `storage.Subagent.ID` as the view key, `DisplayName` as its label, and
`SessionID` to read its transcript. As with the HTTP version, switching the
active view changes rendering only; it must not interrupt a running child.

### Open a child and resume streaming

Read the transcript with `ListMessages`. This is the UI-safe read path: it does
not mark the child's final answer as observed by the parent model.

```go
messages, err := agent.ListMessages(ctx, child.SessionID)
if err != nil {
    return err
}
childStore.ReplaceMessages(child.ID, messages)

if child.Status != storage.SubagentStatusRunning || child.CurrentTurnID == "" {
    return nil
}

run, err := agent.SubagentRun(
    ctx,
    parentSessionID,
    child.ID,
    child.CurrentTurnID,
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
    childStore.ApplyEvent(child.ID, event)
}
for event := range subscription.Events {
    childStore.ApplyEvent(child.ID, event)
}
```

Subscribe before reading retained events, then use the subscription cursor as
the replay fence. This prevents an event from being missed between history
loading and live delivery.

Run the subscription loop in its own goroutine. Keep one cancel function per
`{subagentID, turnID}` and cancel only when that turn ends, the child closes,
or the application shuts down—not when the user switches views.

### Start or continue child work

Start a project-defined child asynchronously:

```go
child, err := agent.StartSubagent(
    ctx,
    parentSessionID,
    parentTurnID,
    "researcher",                  // definition name
    "Compare the storage options", // initial message
    "storage research",            // optional label
)
if err != nil {
    return err
}
childStore.Replace(child.ID, child)
```

Continue an existing child with the same method whether it is idle or running:

```go
child, err = agent.SendSubagentMessage(
    ctx,
    parentSessionID,
    child.ID,
    "Also evaluate migration cost",
)
if err != nil {
    return err
}
childStore.Replace(child.ID, child)
```

An idle child starts a new turn immediately. A running child queues the message
and returns an updated record with another entry in `Pending`. When the current
turn ends, refresh with `ListSubagents`; if `CurrentTurnID` changed, attach to
the new run using the same subscribe-then-replay sequence.

### Handle callbacks and lifecycle changes

`SubscribeSubagentCallbacks` reports completed or failed child turns to the
host. Use it to refresh background view badges and child records without
polling:

```go
for callback := range agent.SubscribeSubagentCallbacks(ctx) {
    if callback.ParentSessionID != parentSessionID {
        continue
    }

    children, err := agent.ListSubagents(ctx, parentSessionID, true)
    if err != nil {
        reportError(err)
        continue
    }
    childStore.Merge(children)
}
```

Do not use `ReadSubagent` merely to render a child transcript. That method is
for advancing the parent's durable observation cursor. Child-view rendering
should use `ListMessages`.

### Interrupt or close a child

Interrupt only the active turn while retaining the child for later messages:

```go
err := agent.InterruptSubagent(
    ctx,
    parentSessionID,
    child.ID,
    "stopped by user",
)
```

Close the child when it should accept no more work:

```go
closed, err := agent.CloseSubagent(ctx, parentSessionID, child.ID)
if err != nil {
    return err
}
childStore.Replace(closed.ID, closed)
```

Closing retains the transcript. Keep the view available as read-only when the
application lists children with `includeClosed` set to `true`.

</TabItem>

</Tabs>
