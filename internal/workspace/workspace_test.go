package workspace

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/layout"
)

type member struct {
	header *tar.Header
	data   []byte
}

func members(t *testing.T, data []byte) map[string]member {
	t.Helper()
	out := map[string]member{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = member{h, body}
	}
}

func names(m map[string]member) []string {
	var out []string
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func mustArchive(t *testing.T) func([]byte, error) []byte {
	return func(data []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
}

func TestConfigArchiveOwnsEveryDirectoryBelowHome(t *testing.T) {
	files := members(t, mustArchive(t)(ConfigArchive(
		"/home/agent/.config/ox", map[string][]byte{"AGENTS.md": []byte("Be brief.")}, nil)))
	want := []string{"home/agent/.config", "home/agent/.config/ox", "home/agent/.config/ox/AGENTS.md"}
	if got := names(files); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if d := files["home/agent/.config"].header; d.Typeflag != tar.TypeDir || d.Mode != 0o755 {
		t.Errorf("home/agent/.config: %+v", d)
	}
	for name, m := range files {
		if m.header.Uid != layout.AgentUID || m.header.Gid != layout.AgentUID {
			t.Errorf("%s is owned by %d:%d", name, m.header.Uid, m.header.Gid)
		}
	}
	if got := files["home/agent/.config/ox/AGENTS.md"]; string(got.data) != "Be brief." || got.header.Mode != 0o644 {
		t.Errorf("AGENTS.md: %q, mode %o", got.data, got.header.Mode)
	}
}

func TestConfigArchiveGivesTheAgentItsStateDirectories(t *testing.T) {
	files := members(t, mustArchive(t)(ConfigArchive(
		"/home/agent/.config/ox", nil,
		[]string{"/home/agent/.local/share/ox", "/home/agent/.local/state/gnat"})))
	want := []string{
		"home/agent/.config", "home/agent/.config/ox", "home/agent/.local",
		"home/agent/.local/share", "home/agent/.local/share/ox",
		"home/agent/.local/state", "home/agent/.local/state/gnat"}
	if got := names(files); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for name, m := range files {
		if m.header.Typeflag != tar.TypeDir || m.header.Uid != layout.AgentUID {
			t.Errorf("%s: %+v", name, m.header)
		}
	}
}

func TestConfigArchiveRejectsDirectoriesOutsideHome(t *testing.T) {
	if _, err := ConfigArchive("/etc/ox", nil, nil); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := ConfigArchive("/home/agent/.claude", nil, []string{"/tmp/x"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestFilesArchiveIsPrivateToTheAgentUser(t *testing.T) {
	secret := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(secret, []byte("KEY=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := members(t, mustArchive(t)(FilesArchive([]string{secret})))
	if got, want := names(files), []string{"opt/gnat-files", "opt/gnat-files/0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := files["opt/gnat-files"].header.Mode; got != 0o700 {
		t.Errorf("directory mode %o", got)
	}
	file := files["opt/gnat-files/0"]
	if file.header.Mode != 0o600 || string(file.data) != "KEY=1\n" {
		t.Errorf("file: mode %o, %q", file.header.Mode, file.data)
	}
	for name, m := range files {
		if m.header.Uid != layout.AgentUID {
			t.Errorf("%s is owned by %d", name, m.header.Uid)
		}
	}
	if StagedPath(0) != layout.FilesDir+"/0" || StagedPath(12) != layout.FilesDir+"/12" {
		t.Errorf("staged paths %s, %s", StagedPath(0), StagedPath(12))
	}
}

func TestFilesArchiveFailsOnAMissingFile(t *testing.T) {
	if _, err := FilesArchive([]string{filepath.Join(t.TempDir(), "gone")}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBinaryArchiveInstallsTheBinaryAsRoot(t *testing.T) {
	files := members(t, mustArchive(t)(BinaryArchive([]byte("ELF"))))
	if got, want := names(files), []string{"opt/gnat/gnat-container"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	m := files["opt/gnat/gnat-container"]
	if m.header.Uid != 0 || m.header.Mode != 0o755 || string(m.data) != "ELF" {
		t.Errorf("binary: uid %d, mode %o, %q", m.header.Uid, m.header.Mode, m.data)
	}
}

func TestBinaryNamesTheMissingArchitecture(t *testing.T) {
	_, err := Binary("riscv64")
	if !errors.Is(err, ErrNoBinary) || !strings.Contains(err.Error(), "make install") ||
		!strings.Contains(err.Error(), "linux/riscv64") {
		t.Fatalf("got %v", err)
	}
}

func TestAgentInstructionsNameTeammatesAndForbidLeakingCredentials(t *testing.T) {
	text := AgentInstructions("You review.\n", []string{"beta", "gamma"}, "OPENROUTER_API_KEY", "demo/alpha/")
	if !strings.HasPrefix(text, "You review.\n\n") {
		t.Errorf("starts %q", text[:20])
	}
	for _, want := range []string{
		"beta, gamma", "--now",
		"`/opt/gnat/gnat-container send TEAMMATE... -m TEXT`",
		"at its next tool call",
		"Never print", "`GNAT_SECRET` and `OPENROUTER_API_KEY`", "You have no GitHub credential",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	alone := AgentInstructions("", nil, "CLAUDE_CODE_OAUTH_TOKEN", "demo/alpha/")
	if strings.Contains(alone, "gnat-container send") || !strings.Contains(alone, "Never print") ||
		!strings.Contains(alone, "You are the only agent in this swarm.") {
		t.Errorf("alone: %s", alone)
	}
}

func TestAgentInstructionsSayWhatTheAgentMayPushAndHowToUsePullRequests(t *testing.T) {
	text := AgentInstructions("x", nil, "GH_TOKEN", "demo/alpha/")
	for _, want := range []string{
		"push only to branches named `demo/alpha/NAME`", "force-pushing",
		"`/opt/gnat/gnat-container pr`", "`gnat:approved`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if text := AgentInstructions("x", nil, "GH_TOKEN", ""); !strings.Contains(text, "so you can't push") {
		t.Error("an agent that can't push isn't told so")
	}
}

func TestAgentInstructionsListTheBaseImagesTools(t *testing.T) {
	text := AgentInstructions("x", nil, "GH_TOKEN", "demo/alpha/")
	if !strings.Contains(text, "without root or sudo") {
		t.Error("missing the sudo note")
	}
	for _, tool := range Tools {
		if !strings.Contains(text, "`"+tool+"`") {
			t.Errorf("missing %s", tool)
		}
	}
}

func TestAgentInstructionsVouchForMessagesReceivedMidTurn(t *testing.T) {
	// The heading the hook gives each message, so the agent recognizes it.
	heading := "Message from the swarm operator, received while you were working"
	for _, teammates := range [][]string{nil, {"beta"}} {
		text := AgentInstructions("x", teammates, "GH_TOKEN", "demo/alpha/")
		if !strings.Contains(text, heading) || !strings.Contains(text, "They are genuine") {
			t.Errorf("teammates %v: no vouching", teammates)
		}
	}
	if !strings.Contains(AgentInstructions("x", []string{"beta"}, "GH_TOKEN", "demo/alpha/"), "operator and your teammates") {
		t.Error("the operator's messages and teammates' aren't both mentioned")
	}
	if strings.Contains(AgentInstructions("x", nil, "GH_TOKEN", "demo/alpha/"), "operator and your teammates") {
		t.Error("a lone agent has no teammates")
	}
}
