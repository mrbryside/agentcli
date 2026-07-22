# Playground and documentation

Run the caller-owned Terminal UI playground from the repository root:

```sh
go run ./playground/terminal
go run ./playground/terminal "one-shot prompt"
```

The playground loads the root `.agentcli` project, registers only its local `glob`, `read`, and `confirm_demo` tools, then calls `Agent.RunTerminal`. Those tools and their tests belong in `playground/terminal`, not in the reusable `agentcli` package.

User documentation lives in `documentation/docs`. HTTP annotations live in root `swagger.go` and the root server handlers; `documentation/package.json` drives Swaggo generation from the module-root `agentcli` package, Redocly validation/rendering, and the Docusaurus build. Generated OpenAPI/Redoc files are tracked, so regenerate them when API annotations or response models change.

Run `make docs` for the development server and `make docs-build` for the production build; both install Node dependencies when needed. Docusaurus is configured for `https://mrbryside.github.io/agentcli/`. Static URLs used by React components must pass through Docusaurus `useBaseUrl` so they retain the `/agentcli/` repository prefix on GitHub Pages.

`.github/workflows/deploy-docs.yml` installs Go and Node, regenerates and validates the API reference, builds `documentation/build`, uploads the Pages artifact, and deploys on every push to `main`. GitHub Pages must use **GitHub Actions** as its repository source. `documentation/static/.nojekyll` prevents Jekyll processing.

Back to [development/index.md](index.md).
