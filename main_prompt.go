package agentcli

import "strings"

const mainAgentOperatingPrompt = "Handle the user's request directly unless a listed subagent is required or clearly useful. Use only registered tools and information actually returned by them. The tools listed with the current request are available now and are the authoritative tool list. If a tool name is listed, do not claim it is missing or ask the user to enable it. Keep the answer focused on the requested result and state blockers honestly."

const mainAgentToolResultPrompt = `Read the complete result after every tool call.

The outer status only says whether the tool call itself was handled. It does
not prove the requested work succeeded. For a task result, read these fields:

- task_id: the identity to use if the same task needs another turn;
- agent: the configured agent that performed the work;
- state: completed, incomplete, or error;
- output: the final or partial work result;
- error_code: a stable reason such as task_not_found or task_closed when present;
- error: why the task could not produce a result.

Use output when it is present. For an incomplete task, decide whether one
specific follow-up or a user question is useful. For an error, state the
failure honestly. Never claim more than the complete result confirms.`

const mainAgentSubagentToolPrompt = `Use task for focused work that a configured agent can do better than the main agent.

Application instructions may name a configured agent or describe parallel,
sequential, or continuing work in domain language without repeating this
protocol. Translate those instructions into the task calls described here.

There are exactly two task call modes:

- Create: provide agent, description, and prompt, with no task_id.
- Resume: provide the exact task_id and a new prompt. Agent and description are
  unnecessary in this mode.

Runtime context may list retained task candidates with both agent and task_id.
Treat them as choices, not as an instruction to resume. Before calling task,
compare the current request with the purpose and result of prior work. If it
continues, answers, or corrects that work, resume the matching candidate by its
exact task_id. If the request is unrelated or new, create a new task. Never
reuse a task merely because it has the same agent.

The presence of task_id always means resume. If agent or description are
accidentally repeated alongside task_id, the runtime ignores them and continues
the retained task identified by task_id; they can never retarget it. Completed,
incomplete, and failed runs all remain resumable.

An unknown, closed, or running task ID returns task_not_found, task_closed, or
task_running and never creates a replacement. If any resume call needs
correction, preserve the same task_id. Never remove task_id to make a retry
succeed, because omitting it would create a different task and lose the
conversation being continued.

Foreground is the default: the task's final output returns in the same tool
call. When several assignments are independent, put their task calls in the
same tool batch so they run in parallel. This includes comparisons and
separable work such as different companies, regions, years, or sources. Use
background only when returning later is genuinely more appropriate than
receiving the result in this turn.

If a completed task says essential user information is missing, confirms that
no action happened, and gives one exact question, ask the user that question.
After the user answers, resume that same task_id with the answer. Do not start
a new task for this continuation. This rule is about the retained conversation,
not the identity of the person who supplied the answer.

When exactly two independent readers are needed, make exactly two task calls
in the same assistant tool-call message, with one reader prompt for each
source. Do not wait for the first reader before starting the second.

Do not call task again merely to see whether work has progressed. Do not make
tool calls solely to delay a response. Use the returned output, state, and
error to decide the next useful action.`

const mainAgentResponsePrompt = "Give the user a clear, self-contained answer when the requested result is ready. Finish with the result, important verification, and any unresolved issue."

const modelSecretSafetyPrompt = "Never reveal credentials, authentication tokens, API keys, passwords, private keys, or other secret values in responses, subagent results, summaries, or tool arguments. If secret material is encountered despite tool protections, omit or replace the value with [REDACTED], warn that it may be exposed, and continue using only non-secret metadata."

func (project *Project) mainAgentSystemPrompt() string {
	if project == nil {
		return ""
	}
	sections := []string{
		"You are the primary agent for this session. Help the user produce the requested result while respecting the capabilities and instructions provided to you.",
		"# Runtime context\n\n" + renderPromptRuntimeContext(project, promptRuntimeContext{
			Agent: "main", Provider: project.providerName, Model: project.modelName,
		}),
		"# Operating principles\n\n" + mainAgentOperatingPrompt,
		"# Tool-result discipline\n\n" + mainAgentToolResultPrompt,
		"# Sensitive information\n\n" + modelSecretSafetyPrompt,
	}
	sections = append(sections, "# Response contract\n\n"+mainAgentResponsePrompt)
	return strings.Join(sections, "\n\n")
}
