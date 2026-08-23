package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/server/deploy"
)

// Deploy API key setting key (stored in the settings table, never returned
// to clients in full).
const deployAPIKeySetting = "deploy_api_key"

// RegisterDeployRoutes registers the /api/deploy/* routes on the mux.
func (s *Server) RegisterDeployRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/deploy/settings", http.HandlerFunc(s.handleDeployGetSettings))
	mux.Handle("PUT /api/deploy/settings", http.HandlerFunc(s.handleDeployPutSettings))
	mux.Handle("POST /api/deploy", http.HandlerFunc(s.handleDeployStart))
	mux.Handle("GET /api/deploy/current", http.HandlerFunc(s.handleDeployCurrent))
	mux.Handle("POST /api/deploy/cancel", http.HandlerFunc(s.handleDeployCancel))
}

// handleDeployGetSettings returns the masked saved API key.
func (s *Server) handleDeployGetSettings(w http.ResponseWriter, r *http.Request) {
	masked := ""
	key, err := s.getDeployAPIKey(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("reading settings: %v", err), http.StatusInternalServerError)
		return
	}
	if key != "" {
		masked = maskKey(key)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"api_key_masked": masked,
		"default_image":  deploy.DefaultImagePrefill,
		"deploy_running": s.deployManager.Busy(),
	})
}

// maskKey shows the first 4 and last 4 characters.
func maskKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("•", len(key))
	}
	return key[:4] + strings.Repeat("•", 12) + key[len(key)-4:]
}

func (s *Server) getDeployAPIKey(ctx context.Context) (string, error) {
	settings, err := s.db.GetAllSettings(ctx)
	if err != nil {
		return "", err
	}
	return settings[deployAPIKeySetting], nil
}

// handleDeployPutSettings saves a new API key.
func (s *Server) handleDeployPutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.APIKey == "" {
		http.Error(w, "api_key is required", http.StatusBadRequest)
		return
	}
	if err := s.db.SetSetting(r.Context(), deployAPIKeySetting, req.APIKey); err != nil {
		s.logger.Error("failed to save deploy api key", "error", err)
		http.Error(w, "failed to save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDeployStart begins a deploy run.
func (s *Server) handleDeployStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VMName     string `json:"vm_name"`
		Image      string `json:"image"`
		ProjectDir string `json:"project_dir"`
		DryRun     bool   `json:"dry_run"`
		APIKey     string `json:"api_key"` // optional; falls back to saved key
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		saved, err := s.getDeployAPIKey(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("reading saved key: %v", err), http.StatusInternalServerError)
			return
		}
		apiKey = saved
	}
	if apiKey == "" {
		http.Error(w, "no exe.dev API key provided or saved", http.StatusBadRequest)
		return
	}

	run, err := s.deployManager.Start(apiKey, req.VMName, req.Image, req.ProjectDir, req.DryRun)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	go s.persistRun(run)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"run_id": run.ID, "vm_name": run.VMName})
}

// persistRun waits for the run to finish and writes the final row.
func (s *Server) persistRun(run *deploy.Run) {
	ctx := context.Background()
	_ = run.Wait(ctx)

	eventsJSON, _ := json.Marshal(run.SnapshotEvents())
	err := s.db.Pool().Tx(ctx, func(ctx context.Context, tx *db.Tx) error {
		_, err := tx.Exec(`INSERT INTO deploy_runs (vm_name, image, project_dir, status, error, events, finished_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			run.VMName, run.Image, run.ProjectDir, run.Status(), run.ErrMsg(), string(eventsJSON), run.FinishedAt())
		return err
	})
	if err != nil {
		s.logger.Error("failed to persist deploy run", "error", err)
	}
}

// handleDeployCurrent streams the active run's events via SSE. If no run is
// active, replays the most recent persisted run once.
func (s *Server) handleDeployCurrent(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	writeEvent := func(evt any) bool {
		b, err := json.Marshal(evt)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	run := s.deployManager.Current()
	if run == nil {
		// No live run: send current status so the UI can settle.
		writeEvent(map[string]any{"type": "idle"})
		return
	}

	// Replay what we have, then follow until done.
	sent := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		events := run.SnapshotEvents()
		for ; sent < len(events); sent++ {
			if !writeEvent(events[sent]) {
				return
			}
		}
		select {
		case <-run.Done():
			final := map[string]any{"type": "finished", "status": run.Status(), "error": run.ErrMsg()}
			if !writeEvent(final) {
				return
			}
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// handleDeployCancel cancels the in-flight run.
func (s *Server) handleDeployCancel(w http.ResponseWriter, r *http.Request) {
	run := s.deployManager.Current()
	if run == nil {
		http.Error(w, "no deploy running", http.StatusConflict)
		return
	}
	if !run.Cancel() {
		http.Error(w, "could not cancel", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelling"})
}

var _ = db.Tx{} // keep import if unused elsewhere
