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
	mux.Handle("POST /api/deploy/delete-vm", http.HandlerFunc(s.handleDeployDeleteVM))
	mux.Handle("GET /api/deploy/analyze", http.HandlerFunc(s.handleDeployAnalyze))
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
		"api_key_masked":       masked,
		"default_image":        deploy.DefaultImagePrefill,
		"deploy_running":       s.deployManager.Busy(),
		"detected_app_ports":   deploy.DetectAppPortsForUI(r.Context(), s.conversationCwdHint()),
		"source_os":            deploy.SourceOSLabel(),
		"full_clone_supported": deploy.FullCloneSupported(),
	})
}

// handleDeployAnalyze returns the dependency report for a directory without
// starting any deploy. GET /api/deploy/analyze?dir=/path
func (s *Server) handleDeployAnalyze(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		http.Error(w, "dir query parameter is required", http.StatusBadRequest)
		return
	}
	rep, err := deploy.AnalyzeProject(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"report":   rep,
		"markdown": deploy.BuildMarkdownReport(rep),
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
		VMName      string `json:"vm_name"`
		Image       string `json:"image"`
		ProjectDir  string `json:"project_dir"`
		Port        int    `json:"port"` // 0 = no specific port handling
		MakePublic bool   `json:"make_public"`
		DryRun     bool   `json:"dry_run"`
		FullClone  bool   `json:"full_clone"`
		SkipSystemd bool  `json:"skip_systemd"`
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

	run, err := s.deployManager.Start(apiKey, req.VMName, req.Image, req.ProjectDir, req.Port, req.MakePublic, req.DryRun, req.FullClone, req.SkipSystemd)
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
		// No run at all: send idle so the UI settles.
		writeEvent(map[string]any{"type": "idle"})
		return
	}

	// If the run already finished (race: pipeline ran fast, SSE connected
	// late), replay all events + the finished event immediately.
	if run.IsDone() {
		for _, e := range run.SnapshotEvents() {
			if !writeEvent(e) {
				return
			}
		}
		final := map[string]any{
			"type":            "finished",
			"status":          run.Status(),
			"error":           run.ErrMsg(),
			"vm_name":         run.VMName,
			"vm_created":      run.VMCreated,
			"markdown_report": run.MarkdownReport,
		}
		writeEvent(final)
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
			final := map[string]any{
				"type":            "finished",
				"status":          run.Status(),
				"error":           run.ErrMsg(),
				"vm_name":         run.VMName,
				"vm_created":      run.VMCreated,
				"markdown_report": run.MarkdownReport,
			}
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

// conversationCwdHint returns the most recent conversation's working directory
// (used to prefill the project dir / detect the app port), or "".
func (s *Server) conversationCwdHint() string {
	convs, err := s.db.ListConversations(context.Background(), 1, 0)
	if err != nil || len(convs) == 0 || convs[0].Cwd == nil {
		return ""
	}
	return *convs[0].Cwd
}

// handleDeployDeleteVM deletes a VM that was created by a failed deploy.
// Requires an API key (saved or provided) with the rm permission.
func (s *Server) handleDeployDeleteVM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VMName string `json:"vm_name"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.VMName == "" {
		http.Error(w, "vm_name is required", http.StatusBadRequest)
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		saved, err := s.getDeployAPIKey(r.Context())
		if err != nil || saved == "" {
			http.Error(w, "no exe.dev API key saved; provide one in the modal first", http.StatusBadRequest)
			return
		}
		apiKey = saved
	}
	c := deploy.NewExecClient(apiKey)
	if err := c.DeleteVM(r.Context(), req.VMName); err != nil {
		s.logger.Error("deploy delete-vm failed", "vm", req.VMName, "error", err)
		msg := err.Error()
		// The exe.dev API returns a permission error when the key lacks the
		// 'rm' command. Give the user actionable guidance.
		if strings.Contains(strings.ToLower(msg), "permission") || strings.Contains(strings.ToLower(msg), "denied") || strings.Contains(strings.ToLower(msg), "forbidden") {
			msg = fmt.Sprintf("the API key may not have permission to delete VMs (rm). Create one with --cmds=whoami,ls,new,rm: %s", msg)
		}
		http.Error(w, msg, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}
