package server

import (
	"agent-core/internal/benchmark"
	"agent-core/internal/project"
	"agent-core/internal/workflow"
	"agent-core/internal/workspace"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
)

func (s *Server) registerExtensionRoutes() {
	s.mux.HandleFunc("GET /v1/workspace", s.handleWorkspaceInfo)
	s.mux.HandleFunc("GET /v1/workspace/entries", s.handleWorkspaceEntries)
	s.mux.HandleFunc("GET /v1/workspace/files/content", s.handleWorkspaceRead)
	s.mux.HandleFunc("PUT /v1/workspace/files/content", s.handleWorkspaceWrite)
	s.mux.HandleFunc("GET /v1/workspace/files/diff", s.handleWorkspaceDiff)
	s.mux.HandleFunc("GET /v1/workspace/git/status", s.handleGitStatus)
	s.mux.HandleFunc("GET /v1/workspace/git/diff", s.handleGitDiff)
	s.mux.HandleFunc("GET /v1/workspace/git/log", s.handleGitLog)
	s.mux.HandleFunc("POST /v1/snapshots", s.handleSnapshotCreate)
	s.mux.HandleFunc("GET /v1/snapshots", s.handleSnapshotList)
	s.mux.HandleFunc("GET /v1/snapshots/{id}", s.handleSnapshotGet)
	s.mux.HandleFunc("POST /v1/snapshots/{id}/restore", s.handleSnapshotRestore)
	s.mux.HandleFunc("GET /v1/skills", s.handleSkillList)
	s.mux.HandleFunc("POST /v1/skills/load", s.handleSkillLoad)
	s.mux.HandleFunc("GET /v1/commands", s.handleCommandList)
	s.mux.HandleFunc("POST /v1/trust/review", s.handleTrustReview)
	s.mux.HandleFunc("POST /v1/trust/grant", s.handleTrustGrant)
	s.mux.HandleFunc("POST /v1/trust/revoke", s.handleTrustRevoke)
	s.mux.HandleFunc("GET /v1/agents", s.handleAgentList)
	s.mux.HandleFunc("POST /v1/agents/{id}/stop", s.handleAgentStop)
	s.mux.HandleFunc("POST /v1/workflows/validate", s.handleWorkflowValidate)
	s.mux.HandleFunc("POST /v1/workflows", s.handleWorkflowStart)
	s.mux.HandleFunc("GET /v1/workflows", s.handleWorkflowList)
	s.mux.HandleFunc("GET /v1/workflows/{id}", s.handleWorkflowGet)
	s.mux.HandleFunc("POST /v1/workflows/{id}/resume", s.handleWorkflowResume)
	s.mux.HandleFunc("POST /v1/workflows/{id}/cancel", s.handleWorkflowCancel)
	s.mux.HandleFunc("GET /v1/runs/{id}/model-calls", s.handleRunModelCalls)
	s.mux.HandleFunc("GET /v1/runs/{id}/tool-calls", s.handleRunToolCalls)
	s.mux.HandleFunc("GET /v1/runs/{id}/changes", s.handleRunChanges)
	s.mux.HandleFunc("POST /v1/runs/{id}/trace/export", s.handleRunTraceExport)
	s.mux.HandleFunc("GET /v1/conversations/{id}/runs", s.handleConversationRuns)
	s.mux.HandleFunc("POST /v1/benchmarks", s.handleBenchmarkStart)
	s.mux.HandleFunc("GET /v1/benchmarks/{id}", s.handleBenchmarkGet)
	s.mux.HandleFunc("POST /v1/benchmarks/{id}/cancel", s.handleBenchmarkCancel)
	s.mux.HandleFunc("GET /v1/config/effective", s.handleEffectiveConfig)
}

func (s *Server) handleWorkspaceInfo(w http.ResponseWriter, _ *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"root": s.services.Workspace.Root(), "project": s.services.Project})
}
func (s *Server) handleWorkspaceEntries(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.services.Workspace.ListDirectory(r.Context(), r.URL.Query().Get("path"), workspace.ListOptions{Offset: offset, Limit: limit})
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleWorkspaceRead(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.services.Workspace.ReadFile(r.Context(), r.URL.Query().Get("path"), workspace.ReadOptions{Offset: offset, Limit: limit})
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleWorkspaceWrite(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	var body struct {
		Path             string `json:"path"`
		Content          string `json:"content"`
		ExpectedRevision string `json:"expected_revision"`
		CreateSnapshot   bool   `json:"create_snapshot"`
	}
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	revision := body.ExpectedRevision
	if revision == "" {
		revision = r.Header.Get("If-Match")
	}
	result, err := s.services.Workspace.WriteFile(r.Context(), body.Path, []byte(body.Content), workspace.WriteOptions{ExpectedRevision: revision, CreateSnapshot: body.CreateSnapshot})
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	result, err := s.services.Workspace.GitDiff(r.Context(), "head", "", r.URL.Query().Get("path"))
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"diff": result})
}
func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	result, err := s.services.Workspace.GitStatus(r.Context())
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	result, err := s.services.Workspace.GitDiff(r.Context(), r.URL.Query().Get("mode"), r.URL.Query().Get("base"), r.URL.Query().Get("path"))
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"diff": result})
}
func (s *Server) handleGitLog(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.services.Workspace.GitLog(r.Context(), r.URL.Query().Get("before"), limit, r.URL.Query().Get("path"))
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	result, err := s.services.Workspace.CreateSnapshot(r.Context())
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusCreated, result)
}
func (s *Server) handleSnapshotList(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.services.Workspace.ListSnapshots(r.Context(), limit)
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleSnapshotGet(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	result, err := s.services.Workspace.Snapshot(r.PathValue("id"))
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	if s.services.Workspace == nil {
		respondError(w, http.StatusServiceUnavailable, "workspace service unavailable")
		return
	}
	var body struct {
		AllowChanges      bool              `json:"allow_changes"`
		ExpectedRevisions map[string]string `json:"expected_revisions"`
	}
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := s.services.Workspace.RestoreSnapshot(r.Context(), r.PathValue("id"), workspace.RestoreOptions{AllowChanges: body.AllowChanges, ExpectedRevisions: body.ExpectedRevisions})
	if err != nil {
		respondWorkspaceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleSkillList(w http.ResponseWriter, _ *http.Request) {
	if s.services.Skills == nil {
		respondJSON(w, http.StatusOK, map[string]any{"skills": []any{}})
		return
	}
	respondJSON(w, http.StatusOK, s.services.Skills.Discover())
}
func (s *Server) handleSkillLoad(w http.ResponseWriter, r *http.Request) {
	if s.services.Skills == nil {
		respondError(w, http.StatusServiceUnavailable, "skills unavailable")
		return
	}
	var body struct{ Name string }
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := s.services.Skills.Load(body.Name)
	if err != nil {
		respondError(w, http.StatusNotFound, "skill not found")
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleCommandList(w http.ResponseWriter, _ *http.Request) {
	if s.services.Commands == nil {
		respondJSON(w, http.StatusOK, map[string]any{"commands": []any{}})
		return
	}
	respondJSON(w, http.StatusOK, s.services.Commands.Discover())
}

type trustRequest struct {
	Workspace    string                `json:"workspace"`
	BundleDigest string                `json:"bundle_digest"`
	Declarations []project.Declaration `json:"declarations"`
}

func (s *Server) trustInput(body trustRequest) (string, []project.Declaration) {
	workspace := body.Workspace
	if workspace == "" {
		workspace = s.services.Project.Workspace
	}
	declarations := body.Declarations
	if len(declarations) == 0 {
		declarations = s.services.Project.Declarations
	}
	return workspace, declarations
}

func (s *Server) handleTrustReview(w http.ResponseWriter, r *http.Request) {
	if s.services.Trust == nil {
		respondError(w, http.StatusServiceUnavailable, "trust unavailable")
		return
	}
	var body trustRequest
	if err := decodeBody(r, &body); err != nil && !errors.Is(err, io.EOF) {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	workspace, declarations := s.trustInput(body)
	status, err := s.services.Trust.Review(workspace, declarations)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "trust review failed")
		return
	}
	respondJSON(w, http.StatusOK, status)
}
func (s *Server) handleTrustGrant(w http.ResponseWriter, r *http.Request) {
	if s.services.Trust == nil {
		respondError(w, http.StatusServiceUnavailable, "trust unavailable")
		return
	}
	var body trustRequest
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	workspace, declarations := s.trustInput(body)
	status, err := s.services.Trust.Grant(workspace, body.BundleDigest, declarations)
	if err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, status)
}
func (s *Server) handleTrustRevoke(w http.ResponseWriter, r *http.Request) {
	if s.services.Trust == nil {
		respondError(w, http.StatusServiceUnavailable, "trust unavailable")
		return
	}
	var body trustRequest
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	workspace, _ := s.trustInput(body)
	if err := s.services.Trust.Revoke(workspace, body.BundleDigest); err != nil {
		respondError(w, http.StatusInternalServerError, "trust revoke failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}
func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
	if s.services.Subagents == nil {
		respondJSON(w, http.StatusOK, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.services.Subagents.List(r.URL.Query().Get("parent"), limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list agents")
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	if s.services.Subagents == nil {
		respondError(w, http.StatusServiceUnavailable, "subagents unavailable")
		return
	}
	if !s.services.Subagents.Stop(r.PathValue("id")) {
		respondError(w, http.StatusNotFound, "active agent not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"stopped": true})
}
func (s *Server) handleWorkflowValidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Definition workflow.Definition `json:"definition"`
	}
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := body.Definition.Validate(s.cfg.Workflow.MaxNodes); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"valid": true, "digest": body.Definition.Digest()})
}
func (s *Server) handleWorkflowStart(w http.ResponseWriter, r *http.Request) {
	if s.services.Workflow == nil {
		respondError(w, http.StatusServiceUnavailable, "workflow unavailable")
		return
	}
	var body struct {
		Definition workflow.Definition `json:"definition"`
	}
	if err := decodeBody(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := s.services.Workflow.Start(r.Context(), body.Definition)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, result)
}
func (s *Server) handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	if s.services.Workflow == nil {
		respondJSON(w, http.StatusOK, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.services.Workflow.List(limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list workflows")
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleWorkflowGet(w http.ResponseWriter, r *http.Request) {
	if s.services.Workflow == nil {
		respondError(w, http.StatusServiceUnavailable, "workflow unavailable")
		return
	}
	result, err := s.services.Workflow.Get(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "workflow not found")
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleWorkflowResume(w http.ResponseWriter, r *http.Request) {
	if s.services.Workflow == nil {
		respondError(w, http.StatusServiceUnavailable, "workflow unavailable")
		return
	}
	result, err := s.services.Workflow.Resume(r.Context(), r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleWorkflowCancel(w http.ResponseWriter, r *http.Request) {
	if s.services.Workflow == nil {
		respondError(w, http.StatusServiceUnavailable, "workflow unavailable")
		return
	}
	if !s.services.Workflow.Cancel(r.PathValue("id")) {
		respondError(w, http.StatusNotFound, "active workflow not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}
func (s *Server) handleRunModelCalls(w http.ResponseWriter, r *http.Request) {
	s.handleTracePart(w, r, "calls")
}
func (s *Server) handleRunToolCalls(w http.ResponseWriter, r *http.Request) {
	s.handleTracePart(w, r, "tools")
}
func (s *Server) handleRunChanges(w http.ResponseWriter, r *http.Request) {
	s.handleTracePart(w, r, "changes")
}
func (s *Server) handleTracePart(w http.ResponseWriter, r *http.Request, part string) {
	if s.services.Recorder == nil {
		respondError(w, http.StatusServiceUnavailable, "model-io is disabled")
		return
	}
	trace, err := s.services.Recorder.ReadTrace(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read trace")
		return
	}
	switch part {
	case "calls":
		respondJSON(w, http.StatusOK, trace.ModelCalls)
	case "tools":
		respondJSON(w, http.StatusOK, trace.Tools)
	default:
		respondJSON(w, http.StatusOK, trace.Changes)
	}
}
func (s *Server) handleRunTraceExport(w http.ResponseWriter, r *http.Request) {
	if s.services.Recorder == nil {
		respondError(w, http.StatusServiceUnavailable, "model-io is disabled")
		return
	}
	directory := filepath.Join(s.cfg.Telemetry.Directory, "exports", r.PathValue("id"))
	manifest, err := s.services.Recorder.ExportTrace(r.PathValue("id"), directory)
	if err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"manifest": manifest, "directory": directory})
}
func (s *Server) handleConversationRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := s.store.ConversationRuns(r.PathValue("id"), limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}
	respondJSON(w, http.StatusOK, result)
}
func (s *Server) handleBenchmarkStart(w http.ResponseWriter, r *http.Request) {
	if s.services.Benchmark == nil {
		respondError(w, http.StatusServiceUnavailable, "benchmark unavailable")
		return
	}
	var manifest benchmark.Manifest
	if err := decodeBody(r, &manifest); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if manifest.Trusted {
		status, err := s.services.Trust.Review(s.services.Project.Workspace, s.services.Project.Declarations)
		if err != nil || !status.Trusted {
			respondError(w, http.StatusForbidden, "trusted benchmark requires current workspace trust")
			return
		}
	}
	plan, err := s.services.Benchmark.Start(manifest)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, plan)
}
func (s *Server) handleBenchmarkGet(w http.ResponseWriter, r *http.Request) {
	if s.services.Benchmark == nil {
		respondError(w, http.StatusServiceUnavailable, "benchmark unavailable")
		return
	}
	plan, err := s.services.Benchmark.Get(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "benchmark plan not found")
		return
	}
	respondJSON(w, http.StatusOK, plan)
}
func (s *Server) handleBenchmarkCancel(w http.ResponseWriter, r *http.Request) {
	if s.services.Benchmark == nil {
		respondError(w, http.StatusServiceUnavailable, "benchmark unavailable")
		return
	}
	if !s.services.Benchmark.Cancel(r.PathValue("id")) {
		respondError(w, http.StatusNotFound, "active benchmark not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}
func (s *Server) handleEffectiveConfig(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"config": s.cfg.Redacted(), "project": s.services.Project})
}

func respondWorkspaceError(w http.ResponseWriter, err error) {
	var serviceErr *workspace.Error
	if errors.As(err, &serviceErr) {
		status := http.StatusBadRequest
		switch serviceErr.Code {
		case workspace.CodeInvalidArgument, workspace.CodeNotDirectory, workspace.CodeNotRegularFile, workspace.CodeBinaryFile, workspace.CodeUnsupported:
			status = http.StatusBadRequest
		case workspace.CodeOutsideWorkspace:
			status = http.StatusForbidden
		case workspace.CodeNotFound:
			status = http.StatusNotFound
		case workspace.CodeConflict, workspace.CodeAlreadyExists:
			status = http.StatusConflict
		case workspace.CodeTooLarge:
			status = http.StatusRequestEntityTooLarge
		case workspace.CodeNotRepository:
			status = http.StatusNotFound
		case workspace.CodeCanceled:
			status = http.StatusConflict
		default:
			status = http.StatusInternalServerError
		}
		respondJSON(w, status, serviceErr)
		return
	}
	respondError(w, http.StatusInternalServerError, "workspace operation failed")
}
