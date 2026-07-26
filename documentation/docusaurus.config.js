const config = {
  title: 'AgentCLI',
  tagline: 'Provider-neutral agent runtime, tools, events, and HTTP APIs for Go',
  favicon: 'img/favicon.svg',
  url: 'https://mrbryside.github.io',
  baseUrl: '/agentcli/',
  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  organizationName: 'mrbryside',
  projectName: 'agentcli',
  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: '/',
          sidebarPath: require.resolve('./sidebars.js'),
          showLastUpdateTime: false,
        },
        blog: false,
        theme: {
          customCss: require.resolve('./src/css/custom.css'),
        },
      },
    ],
  ],
  themeConfig: {
    colorMode: {
      defaultMode: 'dark',
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'AgentCLI',
      items: [
        {
          to: '/',
          label: 'Get started',
          position: 'left',
          activeBaseRegex: '/agentcli/(?:$|getting-started/)',
        },
        {
          to: '/terminal/overview',
          label: 'Terminal UI',
          position: 'left',
          activeBaseRegex: '/terminal/',
        },
        {
          to: '/use-cases/discord-bot',
          label: 'Use cases',
          position: 'left',
          activeBaseRegex: '/use-cases/',
        },
        {to: '/tools/custom-tools', label: 'Custom tools', position: 'left'},
        {
          type: 'dropdown',
          label: 'API',
          position: 'left',
          items: [
            {to: '/api-reference', label: 'API documentation'},
            {to: '/api/sse-events', label: 'Events'},
          ],
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Build',
          items: [
            {label: 'Installation', to: '/'},
            {label: 'Build an agentcli application', to: '/examples/agentcli-application'},
            {label: 'Build a Discord bot', to: '/use-cases/discord-bot'},
            {label: 'Use the Terminal UI', to: '/terminal/overview'},
          ],
        },
        {
          title: 'Reference',
          items: [
            {label: 'API documentation', to: '/api-reference'},
            {label: 'Events', to: '/api/sse-events'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} AgentCLI`,
    },
    prism: {
      additionalLanguages: ['bash', 'go', 'json', 'yaml'],
    },
  },
};

module.exports = config;
