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
agentcli_version=${AGENTCLI_VERSION:-main}
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

mkdir -p "$target/.agentcli/skill/interview" "$target/.agentcli/agent/researcher"
mv "$temporary_tool_read" "$target/tool_read.go"
mv "$temporary_tool_glob" "$target/tool_glob.go"

cat >"$target/go.mod" <<EOF
module $module

go $go_version

require github.com/mrbryside/agentcli v0.0.9
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
		agentcli.WithNonInteractive(false),
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
skills:
  - interview
tools:
  - glob
  - read
---

Understand the requested outcome, use the available capabilities deliberately,
and give the user a clear, self-contained result.
EOF

cat >"$target/.agentcli/config.yaml" <<'EOF'
# OPENAI_API_KEY is loaded from the process environment.
# Keep live provider keys out of this file.
permission_mode: criticalOnly

# Main-agent identity, model, and capability allowlists live in MAIN.md.
providers:
  replace-provider:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    request_timeout: 2m
EOF

cat >"$target/.agentcli/skill/interview/SKILL.md" <<'EOF'
---
name: interview
description: Interview the user to clarify the business requirement before solving it.
---

# Requirements interview

Ask focused questions until the intended outcome, constraints, and success
criteria are clear. Then summarize the agreed requirements.
EOF

cat >"$target/.agentcli/agent/researcher/researcher.md" <<'EOF'
---
name: researcher
description: Use for substantial technical research requiring evidence or trade-off comparison; not for simple answers or code generation.
provider: replace-provider
model: replace-model
tools:
  - glob
  - read
---

You are a research subagent. Identify the important facts, trade-offs, and
uncertainties, then give the parent a concise recommendation.
EOF

if [ "$go_available" = true ]; then
	(cd "$target" && go get "github.com/mrbryside/agentcli@$agentcli_version") || fail 'could not resolve the current agentcli module'
  (cd "$target" && go mod tidy) || fail 'could not resolve Go module dependencies'
  printf '\nCreated agentcli starter in %s (go %s)\n\nNext steps:\n  cd %s\n  export OPENAI_API_KEY=...\n  go run .\n' "$target" "$go_version" "$target"
else
  printf '\nCreated agentcli starter in %s (fallback go %s)\n\nGo was not found. After installing Go:\n  cd %s\n  go mod tidy\n  export OPENAI_API_KEY=...\n  go run .\n' "$target" "$go_version" "$target"
fi
