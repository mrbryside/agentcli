# OpenCode-style subagent tasks

## Goal

Replace the model-facing subagent lifecycle protocol with one task abstraction
that is easy for small and local models to use. Preserve the existing durable
session, permission, confirmation, event, observability, and application-owned
lifecycle infrastructure.

The default path must be:

```text
main agent -> task tool -> subagent session -> final assistant text
           <- completed task result <-
```

Background execution is an explicit or runtime-promoted variation of the same
task. It must not introduce polling, status tools, simulated waiting, or a
second completion-report protocol.

## Model-facing contract

The main agent receives one framework-owned `task` tool with these concepts:

- exact subagent type;
- short description;
- self-contained assignment;
- optional task id only when resuming a previous task;
- optional background execution when enabled by the host.

The result contains only the task id, runtime-owned state, and task output or
error. A foreground call waits for the subagent and returns its result in the
same main-agent provider turn. A background call returns a running result; the
runtime later injects one trusted task-result input into the main-agent session.

Several independent assignments run in parallel when the model emits several
`task` calls in one tool batch. No per-call continuation flag is required.

The model-facing catalog no longer exposes:

- `start_subagent`;
- `send_subagent_message`;
- `report_subagent_result`;
- subagent list, status, wait, or polling tools;
- `continue_main_agent`, `accepted`, `result_delivery`,
  `main_agent_action`, or `result_progress`.

Resuming a task uses its task id through the same `task` tool. The runtime must
validate that the task belongs to the current main-agent session and that the
requested subagent type matches. An unknown or inaccessible task id is an
error; it must never silently create a new task.

## Subagent completion

The subagent's last non-empty final assistant text is the task output. A
subagent does not call a framework completion-report tool.

An agent definition may declare an optional final-result contract when its host
application needs metadata that cannot be inferred safely from natural
language. The subagent still produces one final response and does not call a
completion tool. The runtime validates that response once, extracts its
user/model-facing message as the task output, and publishes the remaining
metadata only as an application event. Application-only metadata is never
inserted into the task tool result or main-agent provider context.

For example, a Discord operator may return one final object containing a
message plus `requires_requester_reply`. The main agent sees only the message.
Discord consumes the boolean event to set or clear its expected-user guard. An
invalid contract is a task error; it does not trigger report-tool or structured
result repair rounds. Agents without a declared result contract, such as a web
summary reader, continue to return ordinary final text.

When the configured provider-step limit is reached:

1. ordinary tools are removed at the provider boundary;
2. tool choice is forced to text only;
3. the model receives one finalization request;
4. the resulting text is returned with runtime-owned `incomplete` state;
5. an empty or failed finalization receives one deterministic runtime error,
   without report-tool repair rounds.

Normal final text produces `completed`. Provider, cancellation, or runtime
failure produces `error`. The runtime owns these states; the model does not
self-classify them.

## Background tasks

Foreground is the default. Background execution uses the same persisted
subagent session and task id.

The runtime owns waiting and completion delivery. It emits one trusted task
result only after the job reaches a terminal state. The main agent is never
asked to poll, send reminders, or call a waiting tool.

Progress is an application/runtime event, not assistant content. Discord may
render or edit progress messages from those events, but the model must not see
or generate progress narration merely to keep a turn alive.

Task-completion application events may include validated metadata from an
agent's optional result contract. Such metadata is for host state only and must
not be serialized into trusted task-result input.

Background work and completion delivery must remain durable across the
application lifecycle to the same extent as existing harness subagent state.
Do not copy OpenCode's process-local background registry limitation.

## Permissions and nesting

Available subagent types are appended to the `task` tool description after
permission filtering. A denied type is omitted rather than shown with an
instruction not to call it.

Subagents receive their configured domain tools and skills. The framework
`task` tool is denied to subagents by default, preserving the current
non-recursive behavior. Existing permission and confirmation escalation to the
main-agent application remains intact.

## Scope retained outside the model protocol

Keep and adapt, rather than replace:

- persisted main-agent and subagent sessions and transcripts;
- permission and confirmation routing;
- response-scope ownership needed for final application delivery;
- retained runtime and system events;
- interruption and cancellation;
- Terminal, HTTP/SSE, playground, init, and Langfuse integration;
- application-owned explicit close operations.

End-of-response tools remain a main-agent application concern and are not part
of subagent completion.

## Discord migration

Discord uses the new `task` tool and simple task results. Its prompts and skills
describe domain routing and expected evidence, not runtime lifecycle fields.

The Discord operator declares a final-result contract with a user-facing
message and `requires_requester_reply`. Discord uses that application-only
boolean to preserve or clear the original requester's continuation guard. A
clarification reply resumes the same task id with fresh Discord context. The
main prompt and skills do not mention this metadata.

Web research may issue several independent foreground task calls in one batch.
Long work may run in the background without requiring a callback-specific
skill. A delivered task result is trusted runtime input and is synthesized once
before final Discord delivery.

Discord progress rendering is driven by task lifecycle events. It must not
publish raw provider errors repeatedly or expose runtime/system prompt language
to users.

## Compatibility and rollout

The implementation may reuse existing manager, storage, and server internals,
but the new model-facing path must not translate task results back into the old
multi-field protocol.

Roll out behind tests in the harness first, then migrate the Terminal,
playground, init templates, HTTP/SSE documentation, and Discord agent. Remove
obsolete prompts, tests, and documentation only after all callers use the task
contract.

Tag and publish the harness only after full cross-repository verification.
Then update Discord to the new tag and rebuild its Compose stack.
