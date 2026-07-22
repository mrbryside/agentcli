# Playground and documentation

Run the caller-owned terminal playground from the repository root:

```sh
go run ./playground/terminal
go run ./playground/terminal "one-shot prompt"
```

The playground loads the root `.agentcli` project, registers only its local `glob`, `read`, and `confirm_demo` tools, then calls `Agent.RunTerminal`. Those tools and their tests belong in `playground/terminal`, not in the reusable `agentcli` package.

User documentation lives in `documentation/docs`. HTTP annotations live in `agentcli/swagger.go` and handlers; `documentation/package.json` drives Swaggo generation from `agentcli`, Redocly validation/rendering, and the Docusaurus build. Generated OpenAPI/Redoc files are tracked, so regenerate them when API annotations or response models change.

Back to [development/index.md](index.md).
