// Package web serves the web UI: a live chat transcript for each agent.
package web

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kkestell/gnat/internal/state"
)

// Port is the port the UI is served on inside the control container.
const Port = 8080

// Events is what the UI needs from the control plane.
//
// TODO(control): internal/control's control plane should implement this; it
// owns the swarm's agents, their setups, and the recorded events: the records
// Watch returns wrap internal/events' events with the agent's name, plus the
// control plane's own swarm_message and swarm_done events.
type Events interface {
	// Agents are the swarm's agents, in order, one column each.
	Agents() []string
	// Setups is each agent's harness, model, and effort (internal/state's
	// Agent), by name.
	Setups() map[string]state.Agent
	// Watch returns a channel of event records, each a JSON object of an agent
	// name and one event: every record for the swarm's agents so far, then
	// each new one. The returned function stops watching.
	Watch() (<-chan string, func())
}

//go:embed page.html
var pageHTML string

//go:embed markdown.js
var markdownJS string

// MarkdownJS is the page's hand-written Markdown renderer.
var MarkdownJS = markdownJS

// Page is the page's HTML, with the Markdown renderer inlined. The agents it
// shows are filled in by page.
var Page = strings.Replace(pageHTML, "__MARKDOWN__", MarkdownJS, 1)

// page is the page, for the swarm's agents.
func page(events Events) string {
	setups := events.Setups()
	agents := events.Agents()
	columns := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		// An agent without a setup is named alone, as the Python swarm's page
		// did; an empty model or effort is the harness's default, null.
		setup, ok := setups[agent]
		if !ok {
			columns = append(columns, map[string]any{"name": agent})
			continue
		}
		columns = append(columns, map[string]any{
			"name":    agent,
			"harness": setup.Harness,
			"model":   optional(setup.Model),
			"effort":  optional(setup.Effort),
		})
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(columns); err != nil {
		encoded.Reset()
		encoded.WriteString("[]")
	}
	return strings.Replace(Page, "__AGENTS__", strings.TrimSuffix(encoded.String(), "\n"), 1)
}

// optional is s, or nil for the empty string, which json encodes as null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Handler serves the page and its event stream for the given events.
func Handler(events Events) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, page(events))
		case "/events":
			stream(events, w, r)
		default:
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "not found\n")
		}
	})
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
func Serve(ctx context.Context, addr string, events Events) error {
	server := &http.Server{Addr: addr, Handler: Handler(events)}
	stop := context.AfterFunc(ctx, func() { server.Close() })
	defer stop()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
