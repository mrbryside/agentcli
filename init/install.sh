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

tools_url=${AGENTCLI_TOOLS_URL:-https://raw.githubusercontent.com/mrbryside/agentcli/main/init/templates/tools.go}
temporary_tools=$(mktemp)
trap 'rm -f "$temporary_tools"' 0 1 2 3 15

printf '%s' 'Go module path (for example github.com/you/my-agent): ' >/dev/tty
IFS= read -r module </dev/tty || fail 'could not read the Go module path'

case "$module" in
  ''|/*|*/|*[!A-Za-z0-9./_-]*) fail "invalid Go module path $module" ;;
esac

[ ! -e "$target" ] || fail "$target already exists; refusing to overwrite it"

curl -fsSL "$tools_url" >"$temporary_tools" || fail 'could not download the starter read and glob tools'

mkdir -p "$target/.agentcli/skill/interview" "$target/.agentcli/agent/researcher"
mv "$temporary_tools" "$target/tools.go"

cat >"$target/go.mod" <<EOF
module $module

go 1.26.3

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
provider: openai
model: gpt-4.1-mini
skills:
  - interview
tools:
  - glob
  - read
---

Understand the requested outcome, use the available capabilities deliberately,
and give the user a clear, self-contained result.
EOF

cat >"$target/.agentcli/config.example.yaml" <<'EOF'
# Copy this file to .agentcli/config.yaml. Keep live provider keys in process
# environment variables; ${VARIABLE} references are expanded at load time.
permission_mode: default

# Main-agent identity, model, and capability allowlists live in MAIN.md.
providers:
  openai:
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
provider: openai
model: gpt-4.1-mini
tools:
  - glob
  - read
---

You are a research subagent. Identify the important facts, trade-offs, and
uncertainties, then give the parent a concise recommendation.
EOF

printf '\nCreated agentcli starter in %s\n\nNext steps:\n  cd %s\n  cp .agentcli/config.example.yaml .agentcli/config.yaml\n  export OPENAI_API_KEY=...\n  go run .\n' "$target" "$target"
