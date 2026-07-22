# Skills and subagents

Skills live at `.agentcli/skill/{name}/SKILL.md`. Their name and description are discovery metadata; full instructions load progressively through the framework-owned `load_skill` tool. The reload policy prevents needless recent duplication while refreshing instructions after age, token, or content-change thresholds.

Subagents live at `.agentcli/agent/{name}/{name}.md` with validated name, description, provider, model, optional skills/tools, and Markdown instructions. Only the root Agent receives framework subagent tools; children cannot recursively spawn children.

Child sessions are always asynchronous. Start/send returns without waiting, and completed, incomplete, or failed outcome arrives through a compact callback containing structured summary/next-step fields, terminal error, and final assistant answer when available. `report_subagent_outcome` is registered automatically only for children; completed requires an explicit successful report, while a missing report safely defaults to incomplete. Lifecycle (`running`, `idle`, `closed`) remains separate from the last-turn outcome.

The model-facing send path hashes normalized content with parent session, parent turn, and child ID. One parent turn may dispatch to a given child once: exact retries return `duplicate`, changed retries return `already_sent`, and neither reaches the mailbox. A new parent turn can send again. Direct Go/HTTP UI sends are intentional user input and retain ordinary FIFO behavior. Parent context reminders describe active children and their last outcome. Avoid polling; status/list operations are for explicit snapshots. The manager enforces parent ownership, queues accepted child follow-ups, supports close/interrupt, and preserves child transcripts for UI views.

Back to [application/index.md](index.md).
