package bashsecure

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
)

// Shell contains the detected user shell details needed to run a command.
type Shell struct {
	Executable string
	Arguments  []string
	ReadRoots  []string
}

// NewCommand creates either a host command or an OS-sandboxed command and
// configures it so cancellation terminates the entire process group.
func NewCommand(ctx context.Context, command string, scope Scope, shell Shell, unrestricted bool) (*exec.Cmd, error) {
	if shell.Executable == "" {
		return nil, fmt.Errorf("shell executable is required")
	}

	var result *exec.Cmd
	var err error
	if unrestricted {
		arguments := append(slices.Clone(shell.Arguments), command)
		result = exec.CommandContext(ctx, shell.Executable, arguments...)
	} else {
		result, err = sandboxedShellCommand(ctx, command, scope, shell)
		if err != nil {
			return nil, err
		}
	}
	configureProcessGroup(result)
	return result, nil
}
