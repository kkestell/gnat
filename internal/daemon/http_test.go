package daemon

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/store"
	"github.com/kkestell/gnat/internal/workspace"
)

func TestHTTPRoutesAndBoundaries(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	load := func() (store.Settings, error) {
		return store.Settings{Harness: "claude", Model: "example", Effort: "high"}, nil
	}
	root := t.TempDir()
	first, err := db.OpenWorkspace(workspace.Workspace{Root: root, CommonDir: root + "/.git", Name: "First <workspace>"}, load)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.OpenWorkspace(workspace.Workspace{Root: "/missing/workspace", Name: "Second"}, load)
	if err != nil {
		t.Fatal(err)
	}
	desc := Descriptor{URL: "http://127.0.0.1:12345", Token: "token", Generation: "generation"}
	stopped := false
	handler := Handler(db, nil, desc, func() { stopped = true })
	cases := []struct {
		method, path, host, origin, auth string
		status                           int
		contains                         string
	}{
		{"GET", "/swarms/" + first.ID, "", "", "", 200, "First &lt;workspace&gt;"},
		{"GET", "/swarms/" + second.ID, "", "", "", 200, "Saved workspace is unavailable"},
		{"GET", "/api/swarms/" + first.ID, "", "", "", 200, first.Orchestrator.ID},
		{"GET", "/api/swarms/" + first.ID + "/state", "", "", "", 200, `"events":[]`},
		{"GET", "/api/swarms/unknown/state", "", "", "", 404, "swarm not found"},
		{"GET", "/api/swarms", "", "", "", 200, second.ID},
		{"GET", "/assets/page.css", "", "", "", 200, "color-scheme:dark"},
		{"GET", "/assets/page.js", "", "", "", 200, "Orchestrator controls"},
		{"GET", "/assets/unknown", "", "", "", 404, "not found"},
		{"GET", "/swarms/unknown", "", "", "", 404, "swarm not found"},
		{"GET", "/swarms/" + first.ID, "attacker.example", "", "", 403, "destination"},
		{"POST", "/api/daemon/stop", "", "https://attacker.example", "Bearer token", 403, "cross-origin"},
		{"POST", "/api/daemon/stop", "", "", "", 401, "credential"},
		{"POST", "/api/daemon/stop", "", "null", "Bearer token", 403, "cross-origin"},
		{"GET", "/api/ready", "", "", "Bearer token", 200, "generation"},
		{"GET", "/api/ready", "", "", "", 401, "credential"},
		{"POST", "/api/daemon/stop", "", "http://127.0.0.1:12345", "Bearer token", 200, "stopping"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.method, tc.path, tc.host, tc.origin, tc.auth), func(t *testing.T) {
			req := httptest.NewRequest(tc.method, desc.URL+tc.path, nil)
			if tc.host != "" {
				req.Host = tc.host
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Authorization", tc.auth)
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if out.Code != tc.status || !strings.Contains(out.Body.String(), tc.contains) {
				t.Fatalf("status %d, body %s", out.Code, out.Body.String())
			}
		})
	}
	if !stopped {
		t.Fatal("stop did not dispatch")
	}
	// A selected route shows only its own orchestrator, and includes other workspace links.
	req := httptest.NewRequest("GET", desc.URL+"/swarms/"+first.ID, nil)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if strings.Contains(out.Body.String(), second.Orchestrator.ConversationID) || !strings.Contains(out.Body.String(), "/swarms/"+second.ID) {
		t.Fatal("workspace view mixed or missing switcher")
	}
}

func TestDescriptorValidation(t *testing.T) {
	dir := t.TempDir()
	if err := prepareRuntime(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runtimeDir(dir), "daemon.json")
	for _, address := range []string{"http://remote:1234", "https://127.0.0.1:1234", "http://127.0.0.1:0", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234?query"} {
		data := fmt.Sprintf(`{"url":%q,"token":"token","generation":"generation"}`, address)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDescriptor(dir); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}
