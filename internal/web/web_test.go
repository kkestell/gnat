package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/state"
)

func TestUILoadsNothingFromElsewhere(t *testing.T) {
	index, err := fs.ReadFile(UI, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{`src="http`, `href="http`, `src="//`, `href="//`, "@import"} {
		if strings.Contains(string(index), needle) {
			t.Errorf("index.html contains %q", needle)
		}
	}
}

type fakeEvents struct {
	agents []string
	setups map[string]state.Agent
}

func (f fakeEvents) Agents() []string               { return f.agents }
func (f fakeEvents) Setups() map[string]state.Agent { return f.setups }
func (f fakeEvents) Watch() (<-chan string, func()) { return nil, func() {} }

func TestAgentsAreServedWithEachSetup(t *testing.T) {
	events := fakeEvents{
		agents: []string{"alpha", "beta"},
		setups: map[string]state.Agent{"alpha": {Harness: "claude", Model: "haiku"}},
	}
	var got []map[string]any
	if err := json.Unmarshal(agents(events), &got); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"harness": "claude", "model": "haiku", "effort": nil, "name": "alpha"},
		{"name": "beta"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestAgentsDoNotEscapeHTMLInSetups(t *testing.T) {
	// json.dumps, which the Python page used, does not escape HTML.
	events := fakeEvents{
		agents: []string{"alpha"},
		setups: map[string]state.Agent{"alpha": {Harness: "claude", Model: "a<b>&c"}},
	}
	if got := string(agents(events)); !strings.Contains(got, `"model":"a<b>&c"`) {
		t.Errorf("the agents escaped HTML: %s", got)
	}
}

// watchingEvents is Events with a hand-fed stream, for the HTTP tests.
type watchingEvents struct {
	agents  []string
	setups  map[string]state.Agent
	records chan string
	stopped chan struct{}
}

func (w *watchingEvents) Agents() []string               { return w.agents }
func (w *watchingEvents) Setups() map[string]state.Agent { return w.setups }
func (w *watchingEvents) Watch() (<-chan string, func()) {
	return w.records, func() { close(w.stopped) }
}

func TestHandlerServesThePageAndTheEventStream(t *testing.T) {
	events := &watchingEvents{
		agents:  []string{"alpha"},
		setups:  map[string]state.Agent{"alpha": {Harness: "claude", Model: "haiku"}},
		records: make(chan string, 1),
		stopped: make(chan struct{}),
	}
	server := httptest.NewServer(Handler(events, Swarm{}))
	defer server.Close()

	for path, file := range map[string]string{"/": "index.html", "/index.js": "index.js", "/index.css": "index.css"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		want, _ := fs.ReadFile(UI, file)
		if response.StatusCode != http.StatusOK || !bytes.Equal(body, want) {
			t.Errorf("GET %s = %d, not %s", path, response.StatusCode, file)
		}
	}

	response, err := http.Get(server.URL + "/agents")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("GET /agents content type = %q", contentType)
	}
	if !bytes.Equal(body, agents(events)) {
		t.Errorf("GET /agents = %s", body)
	}

	response, err = http.Get(server.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || string(body) != "not found\n" {
		t.Errorf("GET /nope = %d %q", response.StatusCode, body)
	}

	response, err = http.Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Errorf("GET /events content type = %q", contentType)
	}
	reader := bufio.NewReader(response.Body)
	record := `{"agent":"alpha","event":{"type":"message","text":"hi"}}`
	events.records <- record
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "data: "+record+"\n" {
		t.Errorf("stream sent %q", line)
	}
	if blank, _ := reader.ReadString('\n'); blank != "\n" {
		t.Errorf("stream did not end its event with a blank line: %q", blank)
	}
	response.Body.Close()
	select {
	case <-events.stopped:
	case <-time.After(time.Second):
		t.Error("the disconnecting browser did not stop the watch")
	}
}

func TestSwarmIsServed(t *testing.T) {
	started := time.Date(2026, 10, 21, 9, 30, 0, 0, time.FixedZone("EDT", -4*3600))
	server := httptest.NewServer(Handler(fakeEvents{}, Swarm{Repo: "o/r", Started: started}))
	defer server.Close()
	response, err := http.Get(server.URL + "/swarm")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if want := `{"repo":"o/r","started_at":"2026-10-21T13:30:00Z"}` + "\n"; string(body) != want {
		t.Errorf("GET /swarm = %s, want %s", body, want)
	}
}

func TestPullsComeFromGitHub(t *testing.T) {
	var requests []*http.Request
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
		fmt.Fprint(w, `[
			{"number": 3, "title": "Open <b>", "html_url": "https://github.com/o/r/pull/3", "state": "open", "draft": true,
			 "merged_at": null, "head": {"ref": "feature"}, "user": {"login": "bot"}, "updated_at": "2026-10-21T12:00:00Z"},
			{"number": 2, "title": "Merged", "html_url": "https://github.com/o/r/pull/2", "state": "closed", "draft": false,
			 "merged_at": "2026-10-20T12:00:00Z", "head": {"ref": "done"}, "user": {"login": "bot"}, "updated_at": "2026-10-20T12:00:00Z"},
			{"number": 1, "title": "Closed", "html_url": "https://github.com/o/r/pull/1", "state": "closed", "draft": false,
			 "merged_at": null, "head": {"ref": "dropped"}, "user": {"login": "me"}, "updated_at": "2026-10-19T12:00:00Z"}
		]`)
	}))
	defer github.Close()
	server := httptest.NewServer(Handler(fakeEvents{}, Swarm{Repo: "o/r", Token: "secret", GitHub: github.URL}))
	defer server.Close()

	var bodies []string
	for range 2 {
		response, err := http.Get(server.URL + "/pulls")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		bodies = append(bodies, string(body))
	}
	want := `[{"number":3,"title":"Open <b>","url":"https://github.com/o/r/pull/3","state":"open","draft":true,` +
		`"branch":"feature","author":"bot","agent":null,"updated_at":"2026-10-21T12:00:00Z"},` +
		`{"number":2,"title":"Merged","url":"https://github.com/o/r/pull/2","state":"merged","draft":false,` +
		`"branch":"done","author":"bot","agent":null,"updated_at":"2026-10-20T12:00:00Z"},` +
		`{"number":1,"title":"Closed","url":"https://github.com/o/r/pull/1","state":"closed","draft":false,` +
		`"branch":"dropped","author":"me","agent":null,"updated_at":"2026-10-19T12:00:00Z"}]` + "\n"
	if bodies[0] != want || bodies[1] != want {
		t.Errorf("GET /pulls =\n%s\nwant\n%s", bodies[0], want)
	}
	if len(requests) != 1 {
		t.Fatalf("GitHub got %d requests, want 1 (the second reuses the first)", len(requests))
	}
	r := requests[0]
	if r.URL.Path != "/repos/o/r/pulls" || r.URL.Query().Get("state") != "all" {
		t.Errorf("GitHub was asked for %s", r.URL)
	}
	if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
		t.Errorf("GitHub got Authorization %q", auth)
	}
}

func TestPullsReportGitHubFailing(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer github.Close()
	server := httptest.NewServer(Handler(fakeEvents{}, Swarm{Repo: "o/r", GitHub: github.URL}))
	defer server.Close()
	response, err := http.Get(server.URL + "/pulls")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || string(body) != "GitHub answered 404 Not Found\n" {
		t.Errorf("GET /pulls = %d %q", response.StatusCode, body)
	}
}
