# Skills and subagents

**v0.1 task protocol:** configured child agents still have persisted subagent
sessions, but the main model receives only `task`. New work supplies agent,
description, and prompt; same-session resume supplies task_id and prompt.
When task_id is present it authoritatively selects resume mode; repeated
agent/description values are ignored and cannot create or retarget a task.
Foreground returns one result in place; background or foreground-wait
promotion uses Agent-owned exact-once delivery. Child agents cannot call `task`.
`TaskState` is running/completed/incomplete/error; result-contract metadata is
application-only in `SystemTaskCompleted`, and step-limit finalization is
text-only with no `report_subagent_result` repair.

Skills live at `.agentcli/skill/{name}/SKILL.md`. Their name and description are
discovery metadata; full instructions load progressively through the
framework-owned `load_skill` tool. A model may load a skill after selecting it
by description, and any applicable instruction may explicitly require loading
one before the governed action or answer. Tool, subagent, and other capability
descriptions help selection but do not replace a required skill load. Whenever
a load trigger first applies in a turn, the model calls `load_skill` instead of
inferring that a visible historical body is still current. A successful load
from an earlier turn does not satisfy a new load trigger. Each call loads only
the exact skill named in the request; skills are never loaded collectively.
Every successful result uses `status=loaded`, and its `name` applies only to
that named skill. A full load also includes `description` and `instructions`;
a cached load instead returns `instructions_in_context=true`. Both forms
include a plain `message` explaining where the instructions are. A tool result
and later provider steps continue the current turn and do not create another
load trigger by themselves. Each named skill may be loaded at most once per runtime
turn. After it loads, the model must not load that skill again until the next
`<turn_start>` marker. Another skill still requires
its own separate valid trigger and load. A lightweight result with
`instructions_in_context=true` means the named skill's full body is already
available in the conversation context and was not repeated. The loader only
makes that named skill's instructions available; those instructions and
current context determine whether the turn continues, waits, or ends. The
runtime reload policy refreshes instructions after age, token, or
content-change thresholds.

The framework injects a trusted `<turn_start>` system reminder only
on the first provider request of each runtime turn. Later provider steps in
that turn do not receive the marker. The reminder identifies the new turn and
resets the model's turn-scoped loaded-skill set. Later provider steps or tool
results never reset that set. The reminder is ephemeral and is never stored in
the conversation transcript.

Subagents live at `.agentcli/agent/{name}/{name}.md` with validated name, description, provider, model, optional skills/tools, and Markdown instructions. Only the main agent receives framework subagent tools; subagents cannot recursively spawn subagents.

The main-agent subagent prompt separates catalog metadata, trigger selection,
instance addressing, assignment-result handling, results, lifecycle, and
safety. A direct description match may trigger delegation when the focused
work materially benefits from a configured subagent; an applicable instruction
or explicit user request may require delegation independently. Topic overlap
alone and discovery-only questions do not trigger a subagent.

Application `MAIN.md`, skills, and subagent role prompts may stay in domain
language without repeating the task protocol. Naming a configured agent,
requesting parallel or sequential work, or asking to continue the same agent
conversation expresses application policy; the framework prompt explains the
task calls that can implement it.

Prompt ownership is deliberately split:

| Owner | Contract |
| --- | --- |
| `MAIN.md` and skills | Domain policy: when a specialist is required, dependency/ordering requirements, constraints, and desired outcomes. |
| Main task framework prompt | Agent selection, new/resume fields, batching, foreground/background behavior, optional saved-ID continuation mechanics, and task-result reading. |
| Per-call `prompt` | Concrete work and the context required for this child turn. |
| Subagent definition body | Domain role, method, evidence standards, and quality criteria. |
| Subagent framework prompt | Tool/evidence boundaries, secret safety, no nesting, generic final delivery, missing-input behavior, and optional exact result JSON. |

Do not duplicate task schemas, polling/lifecycle mechanics, or generic
final-answer boilerplate in project-owned prompts. Framework rules still apply
when an application instruction or task assignment contains only domain
language.

Subagent permission and Yes/No confirmation questions bubble up to the main agent
session. The manager converts retained subagent admission lifecycle facts into
main agent-addressed events with main agent, subagent, session, turn, call, and decision
identity. `SubscribeSubagentPermissions` and
`SubscribeSubagentConfirmations` are the live paths;
`PendingSubagentPermissions` and `PendingSubagentConfirmations` read durable
pending requests when attaching or reconnecting. The main-session UI answers
through `ResolveSubagentPermission` or `ResolveSubagentConfirmation`. This path
is independent from subagent-completion results, which cannot fire while an
admission decision is unresolved.

The main model uses `task` for every child-agent execution. A new task requires
`agent`, `description`, and `prompt`; a resume uses the exact same-session
`task_id` plus `prompt`. A supplied task ID always wins over create-only fields
and never creates a replacement. A corrected resume must preserve task_id
rather than dropping it and accidentally creating another task.
The framework exposes resume as a capability; current application or user
instructions decide whether a particular request should use it.
Foreground is default and returns final output in the same tool call.
Independent tasks in one batch may run concurrently.

`background:true` returns `running`. `WithTaskForegroundWait` can promote an
unfinished foreground call to the same background path. The Agent owns exactly
one terminal delivery, injecting `<task_result>` at a safe provider boundary
or starting a continuation turn. Models and clients never poll, report a
result, or operate delivery callbacks.

Task state is `running`, `completed`, `incomplete`, or `error`. A child at the
provider-step limit receives no tools and supplies one text-only final response;
the runtime returns it as `incomplete`. Child agents cannot call `task` or use
application `EndResponseScope` tools, so tasks cannot nest.

The persisted child lifecycle is `running`, empty/resumable, or `closed`, and
remains distinct from task result state. Host Go, Terminal, and HTTP APIs may
inspect, message, interrupt, resolve safety decisions for, or explicitly close
those sessions. Those APIs are not model tools. Every completed, incomplete,
or failed run remains resumable until an explicit close.

Each optional result contract extracts a message and validated boolean/string
metadata from one final response. The message is task output; metadata is only
in `SystemTaskCompleted`/`task_completed`, never provider context or
`<task_result>`.

Direct Go, Terminal, and HTTP close paths have the same application-owned
destructive lifecycle and should be bound to explicit user actions. Close is
idempotent. Its first successful transition emits `SystemSubagentClosed` on
the live `SubscribeSystemEvents` stream; the HTTP session stream retains it as
`subagent_closed`. Normal completion and response-scope completion retain the
task instead of closing it. See
[subagent-lifecycle.md](subagent-lifecycle.md) for retention, close ordering,
background reminders, and result accounting.

Back to [application/index.md](index.md).
