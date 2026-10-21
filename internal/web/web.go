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
)

// Port is the port the UI is served on inside the control container.
const Port = 8080

// Setup is each agent's harness, model, and effort, for its column heading.
// Model and Effort are nil for the harness's defaults.
//
// TODO(core): this mirrors an agent's settings in internal/state; use that
// package's type once it exists.
type Setup struct {
	Harness string  `json:"harness"`
	Model   *string `json:"model"`
	Effort  *string `json:"effort"`
}

// Events is what the UI needs from the control plane.
//
// TODO(core): internal/control's control plane should implement this; it owns
// the swarm's agents, their setups, and the recorded events.
type Events interface {
	// Agents are the swarm's agents, in order, one column each.
	Agents() []string
	// Setups is each agent's harness, model, and effort, by name.
	Setups() map[string]Setup
	// Watch returns a channel of event records, each a JSON object of an agent
	// name and one event: every record for the swarm's agents so far, then
	// each new one. The returned function stops watching.
	//
	// TODO(core): the event format is internal/events'; the control plane adds
	// its swarm_message and swarm_done events.
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

// column is one agent's entry in the page's __AGENTS__, embedding Setup so an
// agent without a setup is named alone, as the Python swarm's page did.
type column struct {
	*Setup
	Name string `json:"name"`
}

// page is the page, for the swarm's agents.
func page(events Events) string {
	setups := events.Setups()
	agents := events.Agents()
	columns := make([]column, 0, len(agents))
	for _, agent := range agents {
		setup, ok := setups[agent]
		if !ok {
			columns = append(columns, column{Name: agent})
			continue
		}
		columns = append(columns, column{Setup: &setup, Name: agent})
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
