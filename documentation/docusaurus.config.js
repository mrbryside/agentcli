const config = {
  title: 'Harness Agent CLI',
  tagline: 'Provider-neutral agent runtime, tools, events, and HTTP APIs for Go',
  favicon: 'img/favicon.svg',
  url: 'https://example.invalid',
  baseUrl: '/',
  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  organizationName: 'harness-api',
  projectName: 'harness-api',
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
      title: 'Harness Agent CLI',
      items: [
        {to: '/', label: 'Get started', position: 'left'},
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
      copyright: `Copyright © ${new Date().getFullYear()} Harness Agent CLI`,
    },
    prism: {
      additionalLanguages: ['bash', 'go', 'json', 'yaml'],
    },
  },
};

module.exports = config;
