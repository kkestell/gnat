package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/v2/core"
	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
)

type idleAdapter struct{}

func (idleAdapter) Probe(context.Context, store.Settings) error { return nil }
func (idleAdapter) Run(ctx context.Context, _ *store.Claim, _ func(map[string]any) error) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestBrowserChatAndEventReplay(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := db.OpenWorkspace(workspace.Workspace{Root: t.TempDir(), Name: "chat"}, func() (store.Settings, error) {
		return store.Settings{Harness: "claude", Model: "example", Effort: "high"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.New(db, idleAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	desc := Descriptor{URL: "http://" + listener.Addr().String(), Token: "secret", Generation: "gen"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &http.Server{Handler: Handler(db, service, desc, func() {}), BaseContext: func(net.Listener) context.Context { return ctx }}
	go server.Serve(listener)
	defer server.Close()
	post := func(path, origin, header, body string) int {
		t.Helper()
		req, err := http.NewRequest("POST", desc.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Gnat-Browser", header)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	path := "/api/swarms/" + v.ID + "/actions"
	body := `{"request_id":"one","action":"send","text":"hello"}`
	for _, tc := range []struct {
		origin, header string
		want           int
	}{{desc.URL, "1", 200}, {"https://attacker.example", "1", 403}, {"", "1", 401}, {desc.URL, "", 401}} {
		if got := post(path, tc.origin, tc.header, body); got != tc.want {
			t.Fatal("browser authority", got, tc)
		}
	}
	if got := post("/api/daemon/stop", desc.URL, "1", `{}`); got != 401 {
		t.Fatal("browser got daemon authority", got)
	}
	if got := post(path, desc.URL, "1", body); got != 200 {
		t.Fatal("replay failed", got)
	}
	got, err := db.Snapshot(v.ID)
	if err != nil || len(got.Messages) != 1 {
		t.Fatal("duplicate acceptance", got, err)
	}
	streamCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	req, err := http.NewRequestWithContext(streamCtx, "GET", desc.URL+"/api/swarms/"+v.ID+"/events?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "4")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scan := bufio.NewScanner(res.Body)
	found := false
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e store.Event
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		if e.Sequence <= 4 || e.SwarmID != v.ID {
			t.Fatal("wrong replay", e)
		}
		if e.Payload["type"] == "message_accepted" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing accepted event", scan.Err())
	}
	stop()
	// Interrupt carries the observed turn identity through the browser API.
	deadline := time.Now().Add(3 * time.Second)
	var active string
	for time.Now().Before(deadline) {
		current, err := db.Snapshot(v.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range current.Turns {
			if turn.State == "running" {
				active = turn.ID
			}
		}
		if active != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if active == "" {
		t.Fatal("queued browser input never started")
	}
	if status := post(path, desc.URL, "1", `{"request_id":"missing-turn","action":"interrupt"}`); status != 400 {
		t.Fatal("untargeted interrupt accepted", status)
	}
	interrupt, _ := json.Marshal(map[string]string{"request_id": "interrupt-one", "action": "interrupt", "turn_id": active})
	if status := post(path, desc.URL, "1", string(interrupt)); status != 200 {
		t.Fatal("targeted interrupt refused", status)
	}
	for time.Now().Before(deadline) {
		current, err := db.Snapshot(v.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Orchestrator.Lifecycle == "interrupted" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("targeted browser interruption never settled")
}
