package bashsecure

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

var deniedCommands = map[string]string{
	"rm":        "file deletion",
	"rmdir":     "directory deletion",
	"unlink":    "file deletion",
	"truncate":  "destructive truncation",
	"dd":        "raw copying or device writes",
	"mkfs":      "filesystem formatting",
	"diskutil":  "disk management",
	"fdisk":     "disk management",
	"mount":     "filesystem mounting",
	"umount":    "filesystem unmounting",
	"sudo":      "privilege escalation",
	"su":        "privilege escalation",
	"doas":      "privilege escalation",
	"chown":     "ownership changes",
	"kill":      "process termination",
	"killall":   "process termination",
	"pkill":     "process termination",
	"shutdown":  "system shutdown",
	"reboot":    "system reboot",
	"halt":      "system shutdown",
	"poweroff":  "system shutdown",
	"launchctl": "service control",
	"systemctl": "service control",
	"service":   "service control",
	"eval":      "dynamic shell evaluation",
	"source":    "unvalidated nested shell code",
	".":         "unvalidated nested shell code",
	"exec":      "command replacement",
	"command":   "command indirection",
	"builtin":   "command indirection",
	"xargs":     "command indirection",
	"parallel":  "command indirection",
	"nohup":     "detached execution",
	"env":       "command indirection",
	"nice":      "command indirection",
	"chrt":      "command indirection",
	"ionice":    "command indirection",
	"stdbuf":    "command indirection",
	"timeout":   "command indirection",
}

var nestedShellCommands = map[string]struct{}{
	"bash": {}, "dash": {}, "fish": {}, "ksh": {}, "sh": {}, "zsh": {},
}

// Validate rejects dynamic, destructive, or out-of-scope Bash commands.
func Validate(command string, scope Scope) error {
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "bash-tool")
	if err != nil {
		return fmt.Errorf("invalid shell syntax: %w", err)
	}
	var policyErr error
	syntax.Walk(program, func(node syntax.Node) bool {
		if policyErr != nil {
			return false
		}
		switch value := node.(type) {
		case *syntax.CmdSubst:
			policyErr = fmt.Errorf("command substitution is denied")
			return false
		case *syntax.ProcSubst:
			policyErr = fmt.Errorf("process substitution is denied")
			return false
		case *syntax.FuncDecl:
			policyErr = fmt.Errorf("shell function declarations are denied")
			return false
		case *syntax.Redirect:
			if value.Word == nil {
				policyErr = fmt.Errorf("redirect without a static target is denied")
				return false
			}
			target, static := staticShellWord(value.Word)
			if !static {
				policyErr = fmt.Errorf("dynamic redirect targets are denied")
				return false
			}
			if err := validatePath(target, scope, true); err != nil {
				policyErr = fmt.Errorf("redirect target: %w", err)
				return false
			}
		case *syntax.CallExpr:
			policyErr = validateCall(value, scope)
			return policyErr == nil
		}
		return true
	})
	if policyErr != nil {
		return fmt.Errorf("bash command denied: %w", policyErr)
	}
	return nil
}

func validateCall(call *syntax.CallExpr, scope Scope) error {
	words := make([]string, len(call.Args))
	for index, word := range call.Args {
		value, static := staticShellWord(word)
		if !static {
			return fmt.Errorf("dynamic shell expansion is denied")
		}
		words[index] = value
	}
	for _, assignment := range call.Assigns {
		if assignment.Value == nil {
			continue
		}
		value, static := staticShellWord(assignment.Value)
		if !static {
			return fmt.Errorf("dynamic assignment is denied")
		}
		if err := validatePossiblePath(value, scope); err != nil {
			return err
		}
	}
	if len(words) == 0 {
		return nil
	}

	command := words[0]
	base := filepath.Base(command)
	if _, nestedShell := nestedShellCommands[base]; nestedShell {
		if err := validateStaticShellScript(words[1:], scope); err != nil {
			return err
		}
	}
	if reason, denied := deniedCommands[base]; denied {
		return fmt.Errorf("%q is blocked because it enables %s", base, reason)
	}
	if strings.HasPrefix(base, "mkfs.") {
		return fmt.Errorf("%q is blocked because it enables filesystem formatting", base)
	}
	if filepath.IsAbs(command) {
		if !isSystemExecutable(command) {
			if err := validatePath(command, scope, false); err != nil {
				return fmt.Errorf("executable: %w", err)
			}
		}
	} else if strings.ContainsRune(command, filepath.Separator) {
		if err := validatePath(command, scope, false); err != nil {
			return fmt.Errorf("executable: %w", err)
		}
	}

	if base == "git" {
		if err := validateGitCommand(words[1:]); err != nil {
			return err
		}
	}
	if base == "find" {
		for _, argument := range words[1:] {
			if argument == "-delete" || argument == "-exec" || argument == "-execdir" || argument == "-ok" || argument == "-okdir" {
				return fmt.Errorf("find action %q is denied", argument)
			}
		}
	}
	for _, argument := range words[1:] {
		if err := validatePossiblePath(argument, scope); err != nil {
			return err
		}
	}
	return nil
}

func validateStaticShellScript(arguments []string, scope Scope) error {
	if len(arguments) == 0 {
		return fmt.Errorf("nested shell execution requires a static project or temporary script")
	}
	index := 0
	if arguments[index] == "--" {
		index++
	}
	if index >= len(arguments) || strings.HasPrefix(arguments[index], "-") {
		return fmt.Errorf("nested shell flags, commands, and interactive execution are denied")
	}
	if err := validatePath(arguments[index], scope, false); err != nil {
		return fmt.Errorf("nested shell script: %w", err)
	}
	return nil
}

func staticShellWord(word *syntax.Word) (string, bool) {
	var result strings.Builder
	for _, part := range word.Parts {
		switch value := part.(type) {
		case *syntax.Lit:
			result.WriteString(value.Value)
		case *syntax.SglQuoted:
			result.WriteString(value.Value)
		case *syntax.DblQuoted:
			for _, quotedPart := range value.Parts {
				literal, ok := quotedPart.(*syntax.Lit)
				if !ok {
					return "", false
				}
				result.WriteString(literal.Value)
			}
		default:
			return "", false
		}
	}
	return result.String(), true
}

func validateGitCommand(arguments []string) error {
	for index, argument := range arguments {
		switch argument {
		case "clean":
			return fmt.Errorf("git clean is denied because it deletes untracked files")
		case "reset":
			for _, later := range arguments[index+1:] {
				if later == "--hard" || later == "--merge" || later == "--keep" {
					return fmt.Errorf("git reset %s is denied because it discards worktree state", later)
				}
			}
		}
	}
	return nil
}

func validatePossiblePath(argument string, scope Scope) error {
	if strings.Contains(argument, "://") {
		parsed, err := url.Parse(argument)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("invalid network URL %q", argument)
		}
		if scheme := strings.ToLower(parsed.Scheme); scheme != "http" && scheme != "https" {
			return fmt.Errorf("URL scheme %q is not allowed", parsed.Scheme)
		}
		return nil
	}
	if strings.HasPrefix(argument, "-") {
		if _, value, found := strings.Cut(argument, "="); found {
			return validatePossiblePath(value, scope)
		}
		return nil
	}
	if strings.HasPrefix(argument, "~") {
		return fmt.Errorf("home-relative path %q is outside the allowed folders", argument)
	}
	if filepath.IsAbs(argument) || strings.ContainsRune(argument, filepath.Separator) || argument == "." || argument == ".." {
		return validatePath(argument, scope, true)
	}
	return nil
}

func validatePath(path string, scope Scope, allowMissing bool) error {
	if path == "/dev/null" {
		return nil
	}
	_, err := scope.ResolvePath(path, allowMissing)
	return err
}

func isSystemExecutable(path string) bool {
	clean := filepath.Clean(path)
	for _, root := range []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/usr/local/bin", "/opt/homebrew/bin"} {
		if PathWithin(clean, root) {
			return true
		}
	}
	return false
}
