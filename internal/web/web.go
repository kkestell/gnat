// Package web serves the web UI, a live dashboard of the swarm's agents with each
// one's full transcript; the UI itself is built from ui/.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// Events is what the UI needs from the control plane.
//
// TODO(control): internal/control's control plane should implement this; it
// owns the swarm's agents, their setups, and the recorded events: the records
// Watch returns wrap internal/events' events with the agent's name, plus the
// control plane's own swarm_message and swarm_done events.
type Events interface {
	// Agents are the swarm's agents, in order, one card each.
	Agents() []string
	// Setups is each agent's harness, model, and effort (internal/state's
	// Agent), by name.
	Setups() map[string]state.Agent
	// Watch returns a channel of event records, each a JSON object of an agent
	// name and one event: every record for the swarm's agents so far, then
	// each new one. The returned function stops watching.
	Watch() (<-chan string, func())
}

// dist is the built UI (ui/, `make ui`): the page, its script, and its styles.
//
//go:embed dist
var dist embed.FS

// UI is the built UI's files, by name, such as index.html.
var UI, _ = fs.Sub(dist, "dist")

// agents is the JSON of the swarm's agents, each with its harness, model, and
// effort, for the UI.
func agents(events Events) []byte {
	setups := events.Setups()
	names := events.Agents()
	agents := make([]map[string]any, 0, len(names))
	for _, agent := range names {
		// An agent without a setup is named alone, as the Python swarm's page
		// did; an empty model or effort is the harness's default, null.
		setup, ok := setups[agent]
		if !ok {
			agents = append(agents, map[string]any{"name": agent})
			continue
		}
		agents = append(agents, map[string]any{
			"name":    agent,
			"harness": setup.Harness,
			"model":   optional(setup.Model),
			"effort":  optional(setup.Effort),
		})
	}
	encoded, err := protocol.Encode(agents)
	if err != nil {
		return []byte("[]\n")
	}
	return encoded
}

// optional is s, or nil for the empty string, which json encodes as null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Swarm is what the UI shows of the swarm itself.
type Swarm struct {
	// Name is the swarm's name, the first part of its agents' branches.
	Name string
	// Repo is the swarm's repository, OWNER/NAME.
	Repo string
	// Started is when the control plane started, at `gnat up`.
	Started time.Time
	// Token authenticates the requests for the repository's pull requests; empty
	// for none.
	Token string
	// GitHub is GitHub's API, GitHubAPI if empty.
	GitHub string
}

// Handler serves the UI, the swarm, its agents and their event stream, and the
// repository's pull requests.
func Handler(events Events, swarm Swarm) http.Handler {
	files := http.FileServerFS(UI)
	pulls := &pulls{swarm: swarm, events: events, client: &http.Client{Timeout: 20 * time.Second}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents":
			w.Header().Set("Content-Type", "application/json")
			w.Write(agents(events))
		case "/swarm":
			writeJSON(w, map[string]string{"repo": swarm.Repo, "started_at": swarm.Started.UTC().Format(time.RFC3339)})
		case "/pulls":
			found, err := pulls.get(r.Context())
			if err != nil {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, "%s\n", err)
				return
			}
			writeJSON(w, found)
		case "/events":
			stream(events, w, r)
		default:
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" {
				name = "index.html"
			}
			if _, err := fs.Stat(UI, name); err != nil {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, "not found\n")
				return
			}
			files.ServeHTTP(w, r)
		}
	})
}

// writeJSON sends value as JSON.
func writeJSON(w http.ResponseWriter, value any) {
	encoded, err := protocol.Encode(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(encoded)
}

// stream sends every event so far, then each new one, until the browser
// disconnects.
func stream(events Events, w http.ResponseWriter, r *http.Request) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	records, unwatch := events.Watch()
	defer unwatch()
	for {
		select {
		case <-r.Context().Done():
			return
		case record, ok := <-records:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", record); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// Serve runs the UI on addr until ctx is cancelled. It returns nil when ctx
// ends the server, and any error that stops it first.
func Serve(ctx context.Context, addr string, events Events, swarm Swarm) error {
	server := &http.Server{Addr: addr, Handler: Handler(events, swarm)}
	stop := context.AfterFunc(ctx, func() { server.Close() })
	defer stop()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
