//go:build darwin

package bashsecure

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"syscall"
)

func sandboxedShellCommand(ctx context.Context, command string, scope Scope, shell Shell) (*exec.Cmd, error) {
	readRoots := strings.Builder{}
	for _, path := range shell.ReadRoots {
		readRoots.WriteString(` (subpath "`)
		readRoots.WriteString(sandboxQuote(path))
		readRoots.WriteString(`")`)
	}
	profile := `(version 1)
(allow default)
(deny file-read* (subpath "/Users") (subpath "/Volumes") (subpath "/var") (subpath "/private/var"))
(allow file-read-metadata)
(allow file-read* (subpath "` + sandboxQuote(scope.ProjectRoot) + `") (subpath "` + sandboxQuote(scope.TemporaryRoot) + `") (subpath "/private/tmp") (subpath "/private/var/select") (subpath "/private/var/db/dyld")` + readRoots.String() + `)
(deny file-write*)
(allow file-write* (subpath "` + sandboxQuote(scope.ProjectRoot) + `") (subpath "` + sandboxQuote(scope.TemporaryRoot) + `") (subpath "/private/tmp") (literal "/dev/null"))`
	shellArguments := append(slices.Clone(shell.Arguments), command)
	arguments := append([]string{"-p", profile, shell.Executable}, shellArguments...)
	return exec.CommandContext(ctx, "/usr/bin/sandbox-exec", arguments...), nil
}

func sandboxQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}
