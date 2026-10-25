package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kkestell/gnat/internal/core"
	"github.com/kkestell/gnat/internal/store"
)

func chatRoutes(mux *http.ServeMux, db *store.Store, service *core.Service) {
	mux.HandleFunc("POST /api/swarms/{id}/actions", func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
			return
		}
		var input struct {
			RequestID string `json:"request_id"`
			Action    string `json:"action"`
			Text      string `json:"text"`
			TurnID    string `json:"turn_id"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid action JSON", http.StatusBadRequest)
			return
		}
		result, err := service.Mutate(r.PathValue("id"), input.RequestID, input.Action, input.Text, input.TurnID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, result)
	})
	mux.HandleFunc("GET /api/swarms/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := db.Snapshot(id); err != nil {
			recordError(w, err)
			return
		}
		cursor := r.URL.Query().Get("after")
		if last := r.Header.Get("Last-Event-ID"); last != "" {
			cursor = last
		}
		after, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			http.Error(w, "valid after cursor required", http.StatusBadRequest)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			events, err := db.Events(id, after)
			if err != nil {
				return
			}
			for _, e := range events {
				data, err := json.Marshal(e)
				if err != nil {
					return
				}
				if _, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Sequence, data); err != nil {
					return
				}
				after = e.Sequence
			}
			if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	})
}
