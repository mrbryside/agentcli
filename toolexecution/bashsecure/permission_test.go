package bashsecure

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mrbryside/agentcli/permission"
)

func TestDescribePermissionClassifiesReadOnlyCommandsAsNonCritical(t *testing.T) {
	scope := testScope(t)
	for _, test := range []struct {
		command string
		network bool
	}{
		{"pwd", false},
		{"pwd && ls", false},
		{"git status", false},
		{"grep needle README.md | head -n 1", false},
		{"curl -L https://example.com", true},
		{"curl -fkI -X GET https://example.com", true},
		{"curl -H 'Accept: application/json' https://example.com", true},
	} {
		t.Run(test.command, func(t *testing.T) {
			description, err := DescribePermission(scope, test.command)
			if err != nil {
				t.Fatal(err)
			}
			if description.Risk == permission.RiskHigh {
				t.Fatalf("risk=%s actions=%v", description.Risk, description.Actions)
			}
			if got := slices.Contains(description.Actions, permission.NetworkAccess); got != test.network {
				t.Fatalf("network=%v actions=%v", got, description.Actions)
			}
		})
	}
}

func TestDescribePermissionKeepsUnknownAndNetworkCommandsCritical(t *testing.T) {
	scope := testScope(t)
	tests := []struct {
		command string
		network bool
	}{
		{"go run ./cmd", false},
		{"chmod 777 abc.sh", false},
		{"bash -c 'printf bad'", false},
		{"PATH=. ls", false},
		{filepath.Join(scope.TemporaryRoot, "tool"), false},
		{"curl -d secret https://example.com", true},
		{"curl -X POST https://example.com", true},
		{"curl -XPOST https://example.com", true},
		{"curl -F file=@abc.sh https://example.com", true},
		{"curl -K curl.conf https://example.com", true},
		{"curl -u user:secret https://example.com", true},
		{"curl -H 'Authorization: Bearer secret' https://example.com", true},
		{"curl https://user:secret@example.com", true},
		{"ssh example.com", true},
		{"git push origin main", true},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			description, err := DescribePermission(scope, test.command)
			if err != nil {
				t.Fatal(err)
			}
			if description.Risk != permission.RiskHigh {
				t.Fatalf("risk=%s", description.Risk)
			}
			if got := slices.Contains(description.Actions, permission.NetworkAccess); got != test.network {
				t.Fatalf("network=%v actions=%v", got, description.Actions)
			}
		})
	}
}

func TestCriticalOnlyAutomaticallyAllowsPwdAndProjectScriptsButAsksForUnknownBash(t *testing.T) {
	scope := testScope(t)
	for _, test := range []struct {
		command string
		want    permission.Outcome
	}{
		{"pwd", permission.OutcomeAllow},
		{"chmod +x abc.sh", permission.OutcomeAllow},
		{"bash abc.sh", permission.OutcomeAllow},
		{"./abc.sh", permission.OutcomeAllow},
		{"curl https://example.com", permission.OutcomeAllow},
		{"go run ./cmd", permission.OutcomeAsk},
	} {
		description, err := DescribePermission(scope, test.command)
		if err != nil {
			t.Fatal(err)
		}
		request := permission.Request{
			Actions: append([]permission.Action{permission.ProcessExecute}, description.Actions...),
			Risk:    description.Risk,
		}
		if got := permission.Evaluate(request, permission.Policy{Mode: permission.CriticalOnly}); got != test.want {
			t.Fatalf("%q outcome=%s want=%s", test.command, got, test.want)
		}
	}
}

func testScope(t *testing.T) Scope {
	t.Helper()
	project := t.TempDir()
	temporary := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "abc.sh"), []byte("#!/bin/sh\nprintf ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(temporary, "tool"), []byte("#!/bin/sh\nprintf temp\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(project, temporary)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
