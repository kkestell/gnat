package daemon

import (
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"os"

	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
)

//go:embed page.html
var pageFS embed.FS
var page = template.Must(template.ParseFS(pageFS, "page.html"))

func Handler(db *store.Store, desc Descriptor, stop func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ready", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, desc.Generation) })
	mux.HandleFunc("POST /api/workspaces/open", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory string `json:"directory"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Directory == "" {
			http.Error(w, "directory is required", http.StatusBadRequest)
			return
		}
		ws, err := workspace.Discover(input.Directory)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		snapshot, err := db.OpenWorkspace(ws, store.LoadSettings)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, snapshot)
	})
	mux.HandleFunc("POST /api/daemon/stop", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"stopping": true}); stop() })
	mux.HandleFunc("GET /api/swarms", func(w http.ResponseWriter, r *http.Request) {
		all, err := db.List()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, all)
	})
	mux.HandleFunc("GET /api/swarms/{id}", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := db.Snapshot(r.PathValue("id"))
		if err != nil {
			recordError(w, err)
			return
		}
		writeJSON(w, snapshot)
	})
	mux.HandleFunc("GET /swarms/{id}", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := db.Snapshot(r.PathValue("id"))
		if err != nil {
			recordError(w, err)
			return
		}
		all, err := db.List()
		if err != nil {
			recordError(w, err)
			return
		}
		locationError := ""
		if info, err := os.Stat(snapshot.Workspace.Root); err != nil || !info.IsDir() {
			locationError = "Saved workspace is unavailable: " + snapshot.Workspace.Root
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, struct {
			Current       store.Snapshot
			Swarms        []store.Snapshot
			LocationError string
		}{snapshot, all, locationError})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		all, err := db.List()
		if err != nil {
			recordError(w, err)
			return
		}
		if len(all) == 0 {
			http.Error(w, "Open a committed Git workspace with gnat.", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, "/swarms/"+all[0].ID, http.StatusSeeOther)
	})
	address, _ := url.Parse(desc.URL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != address.Host {
			http.Error(w, "invalid local destination", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != desc.URL {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" || r.URL.Path == "/api/ready" {
			expected := "Bearer " + desc.Token
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
				http.Error(w, "daemon credential required", http.StatusUnauthorized)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func recordError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "swarm not found", 404)
	} else {
		http.Error(w, err.Error(), 500)
	}
}
