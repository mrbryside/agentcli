//go:build !darwin

package bashsecure

import (
	"context"
	"fmt"
	"os/exec"
)

func sandboxedShellCommand(context.Context, string, Scope, Shell) (*exec.Cmd, error) {
	return nil, fmt.Errorf("secure bash execution is unavailable on this operating system")
}

func configureProcessGroup(*exec.Cmd) {}
