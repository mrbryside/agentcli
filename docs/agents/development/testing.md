# Testing workflow

Run the smallest relevant Go package tests while iterating, then the repository suite:

```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests sit beside each package. Runtime integration tests exercise the full model/tool/channel wiring; `agentcli` tests cover assembly, project validation, terminal/server behavior, skills, permissions, confirmations, and subagents. Tool policy changes require table tests for modes, risk, action, correlation, cancellation, and concurrent sessions.

Live OpenAI integration tests should remain opt-in and separate from deterministic unit/integration tests. Documentation changes should additionally run `npm run build` from `documentation/`, which regenerates Swaggo output, validates with Redocly, renders the API reference, and builds Docusaurus.

Back to [development/index.md](index.md).
