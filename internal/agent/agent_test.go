package agent

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

func TestRunParsesFlagsAfterTheName(t *testing.T) {
	useWorkspace(t, t.TempDir())
	oldRun, oldRequest := runCommand, request
	runCommand = func(name string, args ...string) (string, error) {
		return `{"name": "A", "login": "a", "id": 1}`, nil
	}
	t.Cleanup(func() { runCommand, request = oldRun, oldRequest })
	t.Setenv(state.AgentVar, "")

	var gotName, gotModel, gotEffort string
	var gotHarness harness.Harness
	status := run([]string{
		"--repo", "o/r", "alpha", "--harness", "ox", "--model", "m", "--effort=high",
		"--file", "/opt/x/0:a.txt",
	}, &bytes.Buffer{}, func(name string, h harness.Harness, model, effort string) {
		gotName, gotHarness, gotModel, gotEffort = name, h, model, effort
	})
	if status != 0 {
		t.Fatalf("status = %d", status)
	}
	if gotName != "alpha" || gotHarness.Name() != "ox" || gotModel != "m" || gotEffort != "high" {
		t.Errorf("served %q %v %q %q", gotName, gotHarness, gotModel, gotEffort)
	}
	if os.Getenv(state.AgentVar) != "alpha" {
		t.Errorf("%s = %q", state.AgentVar, os.Getenv(state.AgentVar))
	}
}

func TestRunDefaultsToTheDefaultHarness(t *testing.T) {
	t.Setenv(state.AgentVar, "")
	useWorkspace(t, t.TempDir())
	oldRun := runCommand
	runCommand = func(string, ...string) (string, error) { return `{"login": "a", "id": 1}`, nil }
	t.Cleanup(func() { runCommand = oldRun })
	var got string
	run([]string{"alpha", "--repo", "o/r"}, &bytes.Buffer{},
		func(_ string, h harness.Harness, _, _ string) { got = h.Name() })
	if got != state.DefaultHarness {
		t.Errorf("harness = %q", got)
	}
}

func TestRunUsageErrorsExitTwo(t *testing.T) {
	for name, args := range map[string][]string{
		"no name":         {"--repo", "o/r"},
		"two names":       {"a", "b", "--repo", "o/r"},
		"no repo":         {"alpha"},
		"unknown harness": {"alpha", "--repo", "o/r", "--harness", "nope"},
		"bad file":        {"alpha", "--repo", "o/r", "--file", "nocolon"},
		"unknown flag":    {"alpha", "--repo", "o/r", "--nope"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			status := run(args, &stderr, func(string, harness.Harness, string, string) {
				t.Error("served")
			})
			if status != 2 || !strings.Contains(stderr.String(), "usage:") {
				t.Errorf("status = %d, stderr = %q", status, stderr.String())
			}
		})
	}
}

func TestRunHelpExitsZero(t *testing.T) {
	var stderr bytes.Buffer
	if status := run([]string{"--help"}, &stderr, nil); status != 0 || !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("status = %d, stderr = %q", status, stderr.String())
	}
}

func TestRunReportsAFailedCloneToTheControlPlane(t *testing.T) {
	t.Setenv(state.AgentVar, "")
	useWorkspace(t, t.TempDir())
	oldRun, oldRequest := runCommand, request
	runCommand = func(name string, args ...string) (string, error) {
		return "", os.ErrPermission
	}
	var sent protocol.Request
	request = func(req protocol.Request) (*protocol.Response, error) {
		sent = req
		return protocol.OK(), nil
	}
	t.Cleanup(func() { runCommand, request = oldRun, oldRequest })
	status := run([]string{"alpha", "--repo", "o/r"}, &bytes.Buffer{},
		func(string, harness.Harness, string, string) { t.Error("served") })
	want := protocol.Request{Op: protocol.OpOutput, Agent: "alpha", Text: "error: permission denied\n"}
	if status != 1 || !reflect.DeepEqual(sent, want) {
		t.Errorf("status = %d, sent %+v", status, sent)
	}
}

func TestParseFileFlagKeepsColonsInThePath(t *testing.T) {
	var files fileFlags
	if err := files.Set("/opt/gnat-files/0:dir/a:b"); err != nil {
		t.Fatal(err)
	}
	if want := (stagedFile{"/opt/gnat-files/0", "dir/a:b"}); files[0] != want {
		t.Errorf("file = %+v", files[0])
	}
}
