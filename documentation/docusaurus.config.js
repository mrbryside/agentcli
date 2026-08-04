const config = {
  title: 'AgentCLI',
  tagline: 'Provider-neutral agent runtime, tools, events, and HTTP APIs for Go',
  favicon: 'img/favicon.svg',
  url: 'https://mrbryside.github.io',
  baseUrl: '/agentcli/',
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
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
          label: 'Getting Started',
          position: 'left',
        },
      ],
    },
    footer: {
      style: 'dark',
      copyright: `Copyright © ${new Date().getFullYear()} AgentCLI`,
    },
    prism: {
      additionalLanguages: ['bash', 'go', 'json', 'yaml'],
    },
  },
};

module.exports = config;
