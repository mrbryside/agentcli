#!/bin/sh
# Bootstrap a minimal terminal application built with agentcli.
set -eu

fail() {
  printf '%s\n' "agentcli installer: $*" >&2
  exit 1
}

[ -r /dev/tty ] || fail 'a terminal is required to create a project'

printf '%s' 'Project folder name (for example my-agent): ' >/dev/tty
IFS= read -r folder </dev/tty || fail 'could not read the project folder name'

case "$folder" in
  ''|.|..|*[!A-Za-z0-9._-]*) fail "invalid project folder name $folder" ;;
esac

target=$folder

tool_read_url=${AGENTCLI_TOOL_READ_URL:-https://raw.githubusercontent.com/mrbryside/agentcli/main/init/templates/tool_read.go}
tool_glob_url=${AGENTCLI_TOOL_GLOB_URL:-https://raw.githubusercontent.com/mrbryside/agentcli/main/init/templates/tool_glob.go}
temporary_tool_read=$(mktemp)
temporary_tool_glob=$(mktemp)
trap 'rm -f "$temporary_tool_read" "$temporary_tool_glob"' 0 1 2 3 15

printf '%s' 'Go module path (for example github.com/you/my-agent): ' >/dev/tty
IFS= read -r module </dev/tty || fail 'could not read the Go module path'

case "$module" in
  ''|/*|*/|*[!A-Za-z0-9./_-]*) fail "invalid Go module path $module" ;;
esac

[ ! -e "$target" ] || fail "$target already exists; refusing to overwrite it"

go_version=1.26.3
# Use the newest published semver tag by default. AGENTCLI_VERSION remains
# available for pinning a release or testing an unreleased branch.
agentcli_version=${AGENTCLI_VERSION:-latest}
# Used in go.mod when Go is unavailable and `go get` cannot resolve latest.
agentcli_fallback_version=v0.0.62
agentcli_module_version=$agentcli_fallback_version
case "$agentcli_version" in
  v[0-9]*) agentcli_module_version=$agentcli_version ;;
esac
go_available=false
if command -v go >/dev/null 2>&1; then
  go_available=true
  detected_go_version=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
  case "$detected_go_version" in
    ''|*[!0-9.]*) ;;
    *) go_version=$detected_go_version ;;
  esac
fi

curl -fsSL "$tool_read_url" >"$temporary_tool_read" || fail 'could not download the starter read tool'
curl -fsSL "$tool_glob_url" >"$temporary_tool_glob" || fail 'could not download the starter glob tool'

mkdir -p "$target/.agentcli"
mv "$temporary_tool_read" "$target/tool_read.go"
mv "$temporary_tool_glob" "$target/tool_glob.go"

cat >"$target/go.mod" <<EOF
module $module

go $go_version

require github.com/mrbryside/agentcli $agentcli_module_version
EOF

cat >"$target/main.go" <<'EOF'
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mrbryside/agentcli"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error ·", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	initialPrompt := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	project, err := agentcli.LoadProject(projectRoot)
	if err != nil {
		return fmt.Errorf("load agent project: %w", err)
	}
	agent, err := agentcli.New(ctx,
		agentcli.WithProject(project),
		agentcli.WithNonInteractive(initialPrompt != ""),
		agentcli.WithTool(newGlobTool(projectRoot)),
		agentcli.WithTool(newReadTool(projectRoot)),
	)
	if err != nil {
		return fmt.Errorf("create agent CLI: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, agent.Close()) }()

	return agent.RunTerminal(agentcli.WithTerminalInitialPrompt(initialPrompt))
}
EOF

cat >"$target/.agentcli/MAIN.md" <<'EOF'
---
provider: replace-provider
model: replace-model
tools:
  - glob
  - read
---

You are chatbot.
EOF

cat >"$target/.agentcli/config.yaml" <<'EOF'
permission_mode: criticalOnly

compaction:
  auto: true
  provider: replace-provider
  model: replace-model

providers:
  replace-provider:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    request_timeout: 2m
    models:
      replace-model:
        context_window_tokens: 122880
        max_output_tokens: 66560
EOF

if [ "$go_available" = true ]; then
	(cd "$target" && GOPROXY=direct GONOSUMDB=github.com/mrbryside/agentcli go get "github.com/mrbryside/agentcli@$agentcli_version") || fail 'could not resolve the current agentcli module'
  (cd "$target" && go mod tidy) || fail 'could not resolve Go module dependencies'
  printf '\nCreated agentcli starter in %s (go %s)\n\nNext steps:\n  cd %s\n  # Replace every replace-provider/replace-model placeholder in .agentcli/\n  export API_KEY=...\n  go run .\n' "$target" "$go_version" "$target"
else
  printf '\nCreated agentcli starter in %s (fallback go %s)\n\nGo was not found. After installing Go:\n  cd %s\n  go mod tidy\n  # Replace every replace-provider/replace-model placeholder in .agentcli/\n  export API_KEY=...\n  go run .\n' "$target" "$go_version" "$target"
fi
