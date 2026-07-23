# Bootstrap installer

`init/install.sh` is the one-command starter generator:

```sh
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

It requires a terminal and prompts through `/dev/tty` only for the project
folder and Go module path. It does not request or persist provider credentials
and accepts no required positional arguments. The target must not already
exist. Folder and module names are validated before any project files are
created.

The installer detects `go env GOVERSION` and writes that version to `go.mod`;
when Go is unavailable it falls back to `1.26.3`. With Go available it runs
`go mod tidy` before reporting success. Generated configuration references
`${OPENAI_API_KEY}`, which must be supplied through the process environment;
generated code has no `.env` loader.

Generated `.agentcli/config.yaml` starts in `criticalOnly` mode and defines an
OpenAI-compatible provider under the explicit placeholder alias
`replace-provider`. Every generated agent currently selects
`provider: replace-provider` and `model: replace-model`, so callers must replace
those identities when targeting a real provider/model. The starter includes
`MAIN.md`, an interview skill, and a researcher subagent.

`init/templates/tool_read.go` and `init/templates/tool_glob.go` are downloaded
separately and become `tool_read.go` and `tool_glob.go` in the generated
module. Tests may override their source URLs with
`AGENTCLI_TOOL_READ_URL` and `AGENTCLI_TOOL_GLOB_URL`.

The `read` tool is project-root scoped, rejects sensitive paths and escaping
symlinks, returns UTF-8 text only, and reads at most 2,000 lines and 256 KiB per
call. It uses a 1-based `offset` and returns `next_offset` when truncated. The
`glob` tool supports recursive `**`, does not follow directory symlinks,
omits sensitive paths, defaults to 100 results, and caps results at 500.
Both tools declare low-risk filesystem-read permission; a subagent request is
surfaced to the parent session when the active permission policy requires an
answer.

When changing the installer, run `sh -n init/install.sh`, execute it in a real
PTY against a temporary directory, inspect every generated agent/provider
reference, then run `go mod tidy` and `go test ./...` inside the generated
module. Keep the two template files independently downloadable because the
installer fetches each raw GitHub URL directly.

Back to [development/index.md](index.md).
