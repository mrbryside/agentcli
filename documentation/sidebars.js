module.exports = {
  docs: [
    {
      type: 'category',
      label: 'Getting Started',
      collapsed: false,
      items: [
        'getting-started/installation',
        'getting-started/project-configuration',
      ],
    },
    {
      type: 'category',
      label: 'Build Agents',
      items: [
        'tools/custom-tools',
        'tools/permissions-and-confirmations',
        'capabilities/skills-and-task-agents',
      ],
    },
    {
      type: 'category',
      label: 'Run & Integrate',
      items: [
        'agentcli/runs-and-sessions',
        'terminal/overview',
        'agentcli/server',
        'observability/overview',
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      items: [
        {
          type: 'link',
          label: 'Go API (pkg.go.dev)',
          href: 'https://pkg.go.dev/github.com/mrbryside/agentcli',
        },
        'api/api-reference',
      ],
    },
  ],
};
