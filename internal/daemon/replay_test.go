package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/core"
	"github.com/kkestell/gnat/internal/store"
	"github.com/kkestell/gnat/internal/workspace"
)

func TestSnapshotGapDisconnectAndSwarmReplay(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	open := func() store.Snapshot {
		t.Helper()
		v, err := db.OpenWorkspace(workspace.Workspace{Root: t.TempDir(), Name: "replay"}, func() (store.Settings, error) {
			return store.Settings{Harness: "claude", Model: "fixture", Effort: "high"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	a, b := open(), open()
	service, err := core.New(db, idleAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewUnstartedServer(nil)
	desc := Descriptor{URL: "http://" + server.Listener.Addr().String(), Token: "private"}
	server.Config.Handler = Handler(db, service, desc, func() {})
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mutate := func(id, key, kind, text string) {
		t.Helper()
		if err := request(ctx, desc, "POST", "/api/swarms/"+id+"/actions", map[string]string{"request_id": key, "action": kind, "text": text}, nil); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) store.Snapshot {
		t.Helper()
		var v store.Snapshot
		if err := request(ctx, desc, "GET", "/api/swarms/"+id, nil, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	mutate(a.ID, "pause", "pause", "")
	mutate(b.ID, "pause", "pause", "")
	before := get(a.ID)
	// Accept work after the snapshot but before establishing its subscription.
	mutate(b.ID, "other", "send", "belongs to b")
	mutate(a.ID, "gap", "send", "snapshot gap")
	replay := func(after int64, last string) []store.Event {
		t.Helper()
		expected, err := db.Events(a.ID, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(expected) == 0 {
			t.Fatal("expected replay records")
		}
		streamCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		query := strconv.FormatInt(after, 10)
		if last != "" {
			query = "0"
		}
		req, err := http.NewRequestWithContext(streamCtx, "GET", desc.URL+"/api/swarms/"+a.ID+"/events?after="+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Last-Event-ID", last)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		scanner := bufio.NewScanner(res.Body)
		got := []store.Event{}
		for scanner.Scan() {
			if !strings.HasPrefix(scanner.Text(), "data: ") {
				continue
			}
			var e store.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &e); err != nil {
				t.Fatal(err)
			}
			if e.SwarmID != a.ID || e.Sequence <= after {
				t.Fatal("cross swarm or duplicate replay", e)
			}
			got = append(got, e)
			if len(got) == len(expected) {
				break
			}
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatal("HTTP replay differs from committed records", got, expected, scanner.Err())
		}
		return got
	}
	first := replay(before.Cursor, "")
	cursor := first[len(first)-1].Sequence
	// Closing the subscription leaves accepted work intact; another send is durable
	// while no browser is connected, and retrying its request ID is harmless.
	mutate(a.ID, "offline", "send", "while browser disconnected")
	mutate(a.ID, "offline", "send", "while browser disconnected")
	second := replay(cursor, strconv.FormatInt(cursor, 10))
	current := get(a.ID)
	if len(current.Messages) != 2 || current.Messages[0].State != "queued" || current.Messages[1].State != "queued" || current.Cursor != second[len(second)-1].Sequence {
		t.Fatal("disconnect lost or duplicated input", current)
	}
	if other := get(b.ID); len(other.Messages) != 1 || other.Messages[0].Text != "belongs to b" {
		t.Fatal("mixed swarm input", other)
	}
	all, err := db.Events(a.ID, 0)
	if err != nil || !reflect.DeepEqual(current.Events, all) {
		t.Fatal("snapshot differs from replay", err)
	}
}
