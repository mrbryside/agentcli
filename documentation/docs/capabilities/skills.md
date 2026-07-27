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

Add its exact name to `.agentcli/MAIN.md` or a child definition:

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

Whenever a real load trigger applies, the model calls `load_skill` even when a
matching skill body is already visible in conversation history. The model does
not decide whether that body is fresh; the runtime owns caching. Every
successful result uses `loaded`.

## Repeat and refresh behavior

An unchanged, recently loaded skill call returns a small `loaded` result with
`instructions_in_context=true` instead of repeating its body. This means the
load succeeded and the full instructions are already available in the
conversation context. A successful load from an earlier turn does not satisfy
a new load trigger. The loader only makes instructions available; it does not
decide the turn's next behavior. The default refresh policy returns the full
body again when any condition applies:

- at least 10 turns have passed;
- approximately 12,000 transcript tokens have passed; or
- the skill content hash changed.

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
