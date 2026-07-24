module.exports = {
  docs: [
    {
      type: 'category',
      label: 'Getting started',
      items: [
        'getting-started/installation',
        'getting-started/bootstrap-project',
        'getting-started/project-configuration',
        'getting-started/first-agent',
      ],
    },
    {
      type: 'category',
      label: 'Agent CLI',
      items: [
        'agentcli/runs-and-sessions',
        'agentcli/events-and-history',
        'agentcli/child-views',
        'agentcli/server',
      ],
    },
    {
      type: 'category',
      label: 'Terminal UI',
      items: [
        'terminal/overview',
        'terminal/input-and-streaming',
        'terminal/commands',
        'terminal/subagent-views',
        'terminal/safety-and-troubleshooting',
      ],
    },
    {
      type: 'category',
      label: 'Guardrails',
      items: [
        'guardrails/overview',
        'guardrails/agent-input-output',
        'guardrails/tool-call',
        'guardrails/prompt-contract',
      ],
    },
    {
      type: 'category',
      label: 'Tools and safety',
      items: [
        'tools/custom-tools',
        'tools/input-schemas',
        'tools/permissions-and-confirmations',
        'tools/security',
      ],
    },
    {
      type: 'category',
      label: 'Capabilities',
      items: [
        'capabilities/skills',
        'capabilities/subagents',
      ],
    },
    {
      type: 'category',
      label: 'API reference',
      items: [
        'api/api-reference',
        'api/sse-events',
      ],
    },
    {
      type: 'category',
      label: 'Examples',
      items: [
        'examples/agentcli-application',
        'examples/api-client-integration',
        'examples/terminal-playground',
        'examples/testing',
      ],
    },
  ],
};
