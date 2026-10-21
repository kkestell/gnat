package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// useStateHome points XDG_STATE_HOME at a fresh directory and returns it.
func useStateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func mustLoad(t *testing.T, name string) *Swarm {
	t.Helper()
	s, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustSave(t *testing.T, s *Swarm) {
	t.Helper()
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	useStateHome(t)
	want := &Swarm{
		Name: "demo", Dockerfile: "/x/Dockerfile", Repo: "owner/repo",
		Agents: []Agent{
			{Name: "alpha", Instructions: "Be brief.", Model: "haiku", Harness: "claude"},
			{Name: "beta", Instructions: "x", Effort: "high", Harness: "ox"},
		},
		Files: []File{},
	}
	mustSave(t, want)
	if got := mustLoad(t, "demo"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	names, err := Names()
	if err != nil || !reflect.DeepEqual(names, []string{"demo"}) {
		t.Fatalf("names %v, %v", names, err)
	}
}

func TestOnDiskFormatMatchesPythons(t *testing.T) {
	home := useStateHome(t)
	mustSave(t, &Swarm{Name: "demo", Repo: "o/r", Agents: []Agent{{Name: "a", Instructions: "x", Harness: "claude"}},
		Files: []File{{Host: "/h/.env", Path: ".env"}}})
	data, err := os.ReadFile(filepath.Join(home, "gnat", "swarms", "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "dockerfile": null,
  "repo": "o/r",
  "agents": [
    {
      "name": "a",
      "instructions": "x",
      "model": null,
      "effort": null,
      "harness": "claude"
    }
  ],
  "files": [
    {
      "host": "/h/.env",
      "path": ".env"
    }
  ]
}
`
	if string(data) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", data, want)
	}
}

func TestAgentsSavedWithoutAHarnessRunClaude(t *testing.T) {
	useStateHome(t)
	mustSave(t, &Swarm{Name: "demo", Repo: "o/r"})
	p := filepath.Join(Directory(), "demo.json")
	data := `{"dockerfile": null, "repo": "o/r", "agents": [{"name": "alpha", "instructions": "x", "model": null, "effort": null}]}`
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	s := mustLoad(t, "demo")
	if s.Agents[0].Harness != "claude" {
		t.Fatalf("harness %q", s.Agents[0].Harness)
	}
	if len(s.Files) != 0 {
		t.Fatalf("files %v", s.Files)
	}
}

func TestDelete(t *testing.T) {
	useStateHome(t)
	mustSave(t, &Swarm{Name: "demo", Dockerfile: "Dockerfile", Repo: "o/r"})
	if err := Delete("demo"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Exists("demo"); ok {
		t.Fatal("still exists")
	}
}

func TestLoadMissingSwarm(t *testing.T) {
	useStateHome(t)
	_, err := Load("nope")
	var se *Error
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "no swarm named 'nope'") {
		t.Fatalf("got %v", err)
	}
}

func TestCheckNameRejects(t *testing.T) {
	for _, name := range []string{"", "-x", "a b", "../x", "a\n"} {
		if err := CheckName(name, "swarm name"); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
	for _, name := range []string{"a", "a-b_c.d", "0"} {
		if err := CheckName(name, "swarm name"); err != nil {
			t.Errorf("%q rejected: %v", name, err)
		}
	}
}

func TestAgentLookup(t *testing.T) {
	s := &Swarm{Name: "demo", Dockerfile: "Dockerfile", Repo: "o/r", Agents: []Agent{{Name: "alpha", Instructions: "x"}}}
	if a, err := s.Agent("alpha"); err != nil || a.Name != "alpha" {
		t.Fatalf("got %v, %v", a, err)
	}
	if _, err := s.Agent("beta"); err == nil || !strings.Contains(err.Error(), "no agent 'beta'") {
		t.Fatalf("got %v", err)
	}
}

func TestLogsLiveBesideTheDefinitionsAndGoWithTheSwarm(t *testing.T) {
	home := useStateHome(t)
	mustSave(t, &Swarm{Name: "demo", Dockerfile: "Dockerfile", Repo: "o/r"})
	logs, err := LogsDirectory("demo")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "gnat", "logs", "demo"); logs != want {
		t.Fatalf("got %s, want %s", logs, want)
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "alpha.log"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Delete("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logs remain: %v", err)
	}
}

func TestSwarmWithoutADockerfileRoundTrips(t *testing.T) {
	useStateHome(t)
	mustSave(t, &Swarm{Name: "demo", Repo: "o/r"})
	if d := mustLoad(t, "demo").Dockerfile; d != "" {
		t.Fatalf("dockerfile %q", d)
	}
}

func TestParseFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, ".env")
	got, err := ParseFile(host)
	if err != nil || got != (File{Host: host, Path: ".env"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	got, err = ParseFile(host + ":cfg/app.env")
	if err != nil || got.Path != "cfg/app.env" || got.Host != host {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ParseFile(":x"); err == nil {
		t.Fatal("accepted a file without a host path")
	}
}

func TestParseFileRejectsBadWorkspacePaths(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"/etc/x", "../x", "a/../../x", ".git/hooks/x", "."} {
		if _, err := ParseFile(filepath.Join(dir, "f") + ":" + path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
}

func TestCheckWorkspacePathNormalizes(t *testing.T) {
	if got, err := CheckWorkspacePath("a//b/./c"); err != nil || got != "a/b/c" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestFilesRoundTrip(t *testing.T) {
	useStateHome(t)
	want := &Swarm{Name: "demo", Dockerfile: "D", Repo: "o/r", Agents: []Agent{},
		Files: []File{{Host: "/h/.env", Path: ".env"}}}
	mustSave(t, want)
	if got := mustLoad(t, "demo"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSavedJSONIsValid(t *testing.T) {
	useStateHome(t)
	mustSave(t, &Swarm{Name: "demo", Repo: "o/r"})
	data, _ := os.ReadFile(filepath.Join(Directory(), "demo.json"))
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
}
