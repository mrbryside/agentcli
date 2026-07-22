# Skills and subagents

Skills live at `.agentcli/skill/{name}/SKILL.md`. Their name and description are discovery metadata; full instructions load progressively through the framework-owned `load_skill` tool. The reload policy prevents needless recent duplication while refreshing instructions after age, token, or content-change thresholds.

Subagents live at `.agentcli/agent/{name}/{name}.md` with validated name, description, provider, model, optional skills/tools, and Markdown instructions. Only the root Agent receives framework subagent tools; children cannot recursively spawn children.

Child sessions are always asynchronous. Start/send returns without waiting, and completion, failure, or interruption arrives through a compact callback containing status, terminal error, and final assistant answer when available. Parent context reminders describe active children. Avoid polling; status/list operations are for explicit snapshots. The manager enforces parent ownership, queues child follow-ups, supports close/interrupt, and preserves child transcripts for UI views.

Back to [application/index.md](index.md).
