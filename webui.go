package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"

	"nix-builder-provisioner/metrics"
	"nix-builder-provisioner/provisioner"
)

//go:embed all:webui/dist
var webuiDist embed.FS

func startWebServer(pool *provisioner.Pool, metricsDB *metrics.Store, port string) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pool.Status())
	})

	mux.HandleFunc("GET /api/builds", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		list, err := metricsDB.ListBuilds(limit, offset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		sum, err := metricsDB.Summary()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sum)
	})

	// SPA static files
	distFS, err := fs.Sub(webuiDist, "webui/dist")
	if err != nil {
		log.Printf("webui: failed to sub dist fs: %v", err)
		return
	}
	mux.Handle("/", http.FileServer(http.FS(distFS)))

	addr := ":" + port
	log.Printf("Web dashboard listening on http://0.0.0.0%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("Web server error: %v", err)
	}
}
