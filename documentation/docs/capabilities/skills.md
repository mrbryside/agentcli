---
title: Skills
sidebar_position: 1
---

# Skills

Skills are project-owned instruction packages selected progressively by the
model. A skill is not executable code and cannot register a custom tool.

## File format

Create `.agentcli/skill/<directory>/SKILL.md`:

```markdown
---
name: interview
description: Interview the user to resolve missing requirements before implementation.
---

# Interview workflow

Ask one focused question at a time. Record resolved constraints and summarize
the final decision before implementation begins.
```

Only `name` and `description` are valid frontmatter fields. Provider, model,
and tool selection belong to agent definitions, not skills.

## Allow a skill

Add its exact name to `.agentcli/MAIN.md` or a subagent definition:

```yaml
skills:
  - interview
```

At startup, allowlist names are validated against discovered files.

## Progressive loading

The initial framework system prompt includes only skill names and descriptions.
This lets the model answer questions such as "what skills are available?"
without loading a body.

When applying a skill, the model calls the restricted framework `load_skill`
tool. Its full Markdown instructions become the latest ordinary tool-result
message. The model should not load a skill merely because the user asks for the
catalog or repeats words from its description.

Whenever a real load trigger applies, the model calls `load_skill` for that
exact skill even when a matching body is already visible in conversation
history. The model does not decide whether that body is fresh; the runtime owns
caching. Every successful result uses `status: "loaded"` and names the one
skill that was loaded.

## Repeat and refresh behavior

The full-body result is:

```json
{
  "status": "loaded",
  "name": "interview",
  "description": "Interview the user to resolve missing requirements before implementation.",
  "instructions": "# Interview workflow\n\n...",
  "message": "Skill \"interview\" loaded successfully. This result applies only to \"interview\". Its full instructions are included in this result. Do not load this skill again until a new <turn_start>."
}
```

An unchanged, recently loaded skill call returns a compact result instead of
repeating its body:

```json
{
  "status": "loaded",
  "name": "interview",
  "instructions_in_context": true,
  "message": "Skill \"interview\" loaded successfully. This result applies only to \"interview\". Its full instructions are already available in the conversation. Do not load this skill again until a new <turn_start>."
}
```

Both forms mean that the named skill loaded successfully. The compact form
does not mean that the whole skill catalog loaded; another skill needs its own
call. It also does not instruct the model to perform any particular next
action—the loaded skill contains that workflow.

Tool results and later provider steps do not create another trigger by
themselves. After a named skill loads, the model must not load that skill again
until a new `<turn_start>` marker appears. A different skill still requires its
own valid trigger and load. A successful load from an earlier turn does not
satisfy a new load trigger. The default refresh policy returns the full body
again when any condition applies:

- at least 10 turns have passed;
- approximately 12,000 transcript tokens have passed; or
- the skill content hash changed.

## Tools that require skills

Application tools may set `RequiredSkills` to make the runtime enforce skill
loading instead of relying only on prompt instructions. Every listed skill
must have a successful `load_skill` result in the current turn before the tool
handler can run. Both full loads and `instructions_in_context=true` loads
satisfy the requirement; an earlier turn does not.

If a required skill is missing, the tool returns a successful non-executing
result with `reason=required_skill_not_loaded`, `required_skills`, and
`missing_skills`. The model can load each missing skill and then retry the
tool. The blocked attempt does not consume that tool's response-scope call
budget.

AgentCLI also injects an ephemeral `<turn_start>` system reminder
only on the first provider request of each runtime turn. Later provider steps
do not receive it, so a tool result or another provider round never resets the
turn-scoped loaded-skill set. The reminder is not persisted in conversation
history.

Configure the thresholds:

```go
agentcli.WithSkillReloadPolicy(agentcli.SkillReloadPolicy{
    MaxTurnDistance:  12,
    MaxTokenDistance: 16_000,
})
```

Set a threshold to zero to disable that threshold. Refreshing old instructions
near the newest messages reduces attention loss in long conversations while
the runtime suppresses duplicate bodies for recent matching calls.

## Prompt placement

The catalog stays in the grouped framework system message. Loaded skill bodies
are tool results in conversation history. Consequently, a provider request may
still contain a previous skill body as history, but the runtime avoids creating
a new duplicate result until the refresh policy says it is stale.
