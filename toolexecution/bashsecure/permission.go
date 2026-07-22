package bashsecure

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/mrbryside/agentcli/permission"

	"mvdan.cc/sh/v3/syntax"
)

var lowRiskCommands = map[string]struct{}{
	"basename": {}, "cat": {}, "date": {}, "df": {}, "dirname": {},
	"du": {}, "false": {}, "file": {}, "find": {}, "grep": {},
	"head": {}, "hostname": {}, "id": {}, "ls": {}, "pwd": {},
	"realpath": {}, "rg": {}, "sort": {}, "stat": {}, "tail": {},
	"test": {}, "true": {}, "type": {}, "uname": {}, "uniq": {},
	"wc": {}, "whoami": {}, "[": {},
}

var mediumRiskCommands = map[string]struct{}{
	"cp": {}, "echo": {}, "ln": {}, "mkdir": {}, "mv": {},
	"printf": {}, "tee": {}, "touch": {},
}

var networkCommands = map[string]struct{}{
	"ftp": {}, "nc": {}, "netcat": {}, "ping": {},
	"rsync": {}, "scp": {}, "sftp": {}, "ssh": {}, "telnet": {},
	"wget": {},
}

// PermissionDescription is the security classification of a Bash command.
// Actions contains capabilities in addition to process execution.
type PermissionDescription struct {
	Risk    permission.Risk
	Actions []permission.Action
	Reason  string
}

// DescribePermission conservatively classifies a static Bash command.
func DescribePermission(scope Scope, command string) (PermissionDescription, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return PermissionDescription{Risk: permission.RiskHigh, Reason: "bash requires a non-empty command"}, nil
	}
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "bash-permission")
	if err != nil {
		return PermissionDescription{}, fmt.Errorf("invalid shell syntax: %w", err)
	}

	risk := permission.RiskLow
	network := false
	syntax.Walk(program, func(node syntax.Node) bool {
		switch value := node.(type) {
		case *syntax.Redirect:
			risk = higherRisk(risk, permission.RiskMedium)
		case *syntax.CallExpr:
			words, static := staticCallWords(value)
			if !static || len(words) == 0 {
				risk = permission.RiskHigh
				return true
			}
			if callChangesLookup(value) {
				risk = permission.RiskHigh
			}
			callRisk, usesNetwork := classifyCallRisk(scope, words)
			risk = higherRisk(risk, callRisk)
			network = network || usesNetwork
		}
		return true
	})

	actions := []permission.Action(nil)
	if network {
		actions = append(actions, permission.NetworkAccess)
	}
	reason := "bash command is classified as read-only"
	switch risk {
	case permission.RiskMedium:
		reason = "bash command may modify project files or execute a concrete project-local script"
	case permission.RiskHigh:
		reason = "bash command can execute an unclassified, external, or critical operation"
	}
	if network {
		reason = "bash command may access the network"
	}
	return PermissionDescription{Risk: risk, Actions: actions, Reason: reason}, nil
}

func callChangesLookup(call *syntax.CallExpr) bool {
	for _, assignment := range call.Assigns {
		if assignment.Name == nil {
			continue
		}
		switch assignment.Name.Value {
		case "PATH", "FPATH", "CDPATH", "BASH_ENV", "ENV":
			return true
		}
	}
	return false
}

func staticCallWords(call *syntax.CallExpr) ([]string, bool) {
	words := make([]string, len(call.Args))
	for index, word := range call.Args {
		value, ok := staticShellWord(word)
		if !ok {
			return nil, false
		}
		words[index] = value
	}
	return words, true
}

func classifyCallRisk(scope Scope, words []string) (permission.Risk, bool) {
	command := words[0]
	base := filepath.Base(command)
	if base == "curl" {
		return classifyCurlRisk(words[1:]), true
	}
	if _, ok := networkCommands[base]; ok {
		return permission.RiskHigh, true
	}
	if _, shell := nestedShellCommands[base]; shell {
		if staticProjectShellScript(scope, words[1:]) {
			return permission.RiskMedium, false
		}
		return permission.RiskHigh, false
	}
	if base == "chmod" {
		if addsExecuteToProjectFiles(scope, words[1:]) {
			return permission.RiskMedium, false
		}
		return permission.RiskHigh, false
	}
	if base == "git" {
		return classifyGitRisk(words[1:])
	}
	if filepath.IsAbs(command) || strings.ContainsRune(command, filepath.Separator) {
		if isSystemExecutable(command) {
			if _, ok := lowRiskCommands[base]; ok {
				return permission.RiskLow, false
			}
			if _, ok := mediumRiskCommands[base]; ok {
				return permission.RiskMedium, false
			}
			return permission.RiskHigh, false
		}
		if projectPath(scope, command, false) {
			return permission.RiskMedium, false
		}
		return permission.RiskHigh, false
	}
	if _, ok := lowRiskCommands[base]; ok {
		return permission.RiskLow, false
	}
	if _, ok := mediumRiskCommands[base]; ok {
		return permission.RiskMedium, false
	}
	return permission.RiskHigh, false
}

func classifyCurlRisk(arguments []string) permission.Risk {
	for index, argument := range arguments {
		lower := strings.ToLower(argument)
		if curlCriticalFlag(argument) {
			return permission.RiskHigh
		}
		if (argument == "-X" || lower == "--request") && index+1 < len(arguments) {
			method := strings.ToUpper(arguments[index+1])
			if method != "GET" && method != "HEAD" {
				return permission.RiskHigh
			}
		}
		if strings.HasPrefix(argument, "-X") && len(argument) > 2 {
			method := strings.ToUpper(argument[2:])
			if method != "GET" && method != "HEAD" {
				return permission.RiskHigh
			}
		}
		if strings.HasPrefix(lower, "--request=") {
			method := strings.ToUpper(strings.TrimPrefix(argument, "--request="))
			if method != "GET" && method != "HEAD" {
				return permission.RiskHigh
			}
		}
		if (argument == "-H" || lower == "--header") && index+1 < len(arguments) && curlSensitiveHeader(arguments[index+1]) {
			return permission.RiskHigh
		}
		if strings.HasPrefix(lower, "--header=") && curlSensitiveHeader(strings.TrimPrefix(argument, "--header=")) {
			return permission.RiskHigh
		}
		if parsed, err := url.Parse(argument); err == nil && parsed.IsAbs() && parsed.User != nil {
			return permission.RiskHigh
		}
	}
	return permission.RiskMedium
}

func curlCriticalFlag(argument string) bool {
	for _, flag := range []string{"-d", "-F", "-T", "-K", "-u", "-b", "-c"} {
		if argument == flag || strings.HasPrefix(argument, flag) && len(argument) > len(flag) {
			return true
		}
	}
	if argument == "-n" {
		return true
	}

	lower := strings.ToLower(argument)
	critical := []string{
		"--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode", "--json",
		"--form", "--form-string", "--upload-file", "--config",
		"--netrc", "--netrc-file", "--netrc-optional", "--user", "--proxy-user",
		"--key", "--cert", "--cookie", "--cookie-jar",
	}
	for _, flag := range critical {
		if lower == flag || strings.HasPrefix(lower, flag+"=") {
			return true
		}
	}
	return false
}

func curlSensitiveHeader(header string) bool {
	name, _, found := strings.Cut(strings.ToLower(strings.TrimSpace(header)), ":")
	return found && (name == "authorization" || name == "cookie" || name == "proxy-authorization")
}

func staticProjectShellScript(scope Scope, arguments []string) bool {
	if len(arguments) == 0 {
		return false
	}
	index := 0
	if arguments[index] == "--" {
		index++
	}
	return index < len(arguments) && !strings.HasPrefix(arguments[index], "-") && projectPath(scope, arguments[index], false)
}

func addsExecuteToProjectFiles(scope Scope, arguments []string) bool {
	if len(arguments) < 2 {
		return false
	}
	index := 0
	if arguments[index] == "--" {
		index++
	}
	if index >= len(arguments) {
		return false
	}
	switch arguments[index] {
	case "+x", "a+x", "g+x", "o+x", "u+x", "ug+x", "uo+x", "go+x", "ugo+x":
	default:
		return false
	}
	index++
	if index >= len(arguments) {
		return false
	}
	for _, path := range arguments[index:] {
		if !projectPath(scope, path, false) {
			return false
		}
	}
	return true
}

func projectPath(scope Scope, path string, allowMissing bool) bool {
	resolved, err := scope.ResolvePath(path, allowMissing)
	return err == nil && PathWithin(resolved, scope.ProjectRoot)
}

func classifyGitRisk(arguments []string) (permission.Risk, bool) {
	if len(arguments) == 0 {
		return permission.RiskLow, false
	}
	switch arguments[0] {
	case "status", "diff", "log", "show", "rev-parse", "ls-files", "ls-tree", "cat-file":
		return permission.RiskLow, false
	case "fetch", "pull", "push", "clone":
		return permission.RiskHigh, true
	default:
		return permission.RiskHigh, false
	}
}

func higherRisk(left, right permission.Risk) permission.Risk {
	rank := func(risk permission.Risk) int {
		switch risk {
		case permission.RiskHigh:
			return 3
		case permission.RiskMedium:
			return 2
		default:
			return 1
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	return left
}
