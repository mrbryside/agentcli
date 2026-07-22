module.exports = {
  docs: [
    {
      type: 'category',
      label: 'Getting started',
      items: [
        'getting-started/installation',
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
      label: 'Tools and safety',
      items: [
        'tools/custom-tools',
        'tools/schema-inference',
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
        'examples/complete-application',
        'examples/api-client-integration',
        'examples/terminal-playground',
        'examples/testing',
      ],
    },
  ],
};
