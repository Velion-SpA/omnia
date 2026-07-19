package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// OBL-04: operator-gated controls for the per-project sync pause. Enforcement
// (IsProjectSyncEnabled gating pushes with a 409) is already wired at the chunk
// and mutation push paths (cloudserver.go / mutations.go); until now nothing
// called SetProjectSyncEnabled, so an operator had no way to actually pause a
// runaway/abusive project. These two routes are the missing CONTROL side.

// projectSyncControlAdminStore is the store capability needed to pause/resume a
// project and read its current control state. *cloudstore.CloudStore satisfies
// it; detected via type assertion (the same capability-detection pattern as
// teamsAdminStore / adminDashboardStore) so the core ChunkStore interface is NOT
// extended.
type projectSyncControlAdminStore interface {
	SetProjectSyncEnabled(project string, enabled bool, updatedBy, reason string) error
	GetProjectSyncControl(project string) (*cloudstore.ProjectSyncControl, error)
	// ListProjectSyncControlsMap is the batched equivalent of calling
	// GetProjectSyncControl once per known project — the Admin Projects page
	// (handleAdminProjectsPage) previously did exactly that inside its per-row
	// loop, an N+1 flagged by the 2026-07-19 performance audit. See
	// cloudstore.CloudStore.ListProjectSyncControlsMap.
	ListProjectSyncControlsMap(ctx context.Context) (map[string]cloudstore.ProjectSyncControl, error)
}

// Compile-time assertion: the concrete store must satisfy the seam.
var _ projectSyncControlAdminStore = (*cloudstore.CloudStore)(nil)

func (s *CloudServer) projectSyncControlStore() (projectSyncControlAdminStore, bool) {
	pcs, ok := s.store.(projectSyncControlAdminStore)
	return pcs, ok
}

// operatorActor resolves a human-readable identity for the audit trail: the
// account username when the operator is an admin-flagged account (OBL-16), or
// the generic "operator" label for the OMNIA_CLOUD_ADMIN bearer/session path,
// which carries no per-request account identity. Mirrors the identity
// resolution requireOperator already performs via dashboardSessionClaims.
func (s *CloudServer) operatorActor(r *http.Request) string {
	if claims, operator := s.dashboardSessionClaims(r); operator && claims != nil {
		if u := strings.TrimSpace(claims.Username); u != "" {
			return u
		}
		if strings.TrimSpace(claims.AccountID) != "" {
			return claims.AccountID
		}
	}
	return "operator"
}

// pauseProjectInput is the optional body for POST /admin/projects/{project}/pause.
type pauseProjectInput struct {
	PausedReason string `json:"paused_reason"`
}

// parsePausedReason reads the optional paused_reason from a JSON body (CLI/API)
// or an HTMX form. An absent/empty body is fine — SetProjectSyncEnabled stores an
// empty reason as NULL.
func (s *CloudServer) parsePausedReason(w http.ResponseWriter, r *http.Request) (string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if isFormContentType(r) {
		if err := r.ParseForm(); err != nil {
			return "", err
		}
		return strings.TrimSpace(r.PostFormValue("paused_reason")), nil
	}
	var in pauseProjectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(in.PausedReason), nil
}

// handleAdminPauseProject handles POST /admin/projects/{project}/pause. Operator
// (or account admin — OBL-16) only, exactly like the rest of the Admin section.
func (s *CloudServer) handleAdminPauseProject(w http.ResponseWriter, r *http.Request) {
	s.setProjectSyncEnabled(w, r, false)
}

// handleAdminResumeProject handles POST /admin/projects/{project}/resume.
func (s *CloudServer) handleAdminResumeProject(w http.ResponseWriter, r *http.Request) {
	s.setProjectSyncEnabled(w, r, true)
}

// setProjectSyncEnabled backs both the pause and resume routes: it authorizes,
// resolves the project + optional reason, calls the already-implemented
// SetProjectSyncEnabled (this is the missing caller OBL-04 wires in), and audits
// the operator ACTION itself. This is distinct from the REQ-404/405 audit trail
// emitted when a push is later REJECTED because the project is paused — that
// enforcement path is untouched.
func (s *CloudServer) setProjectSyncEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if !s.requireOperator(w, r) {
		return
	}
	pcs, ok := s.projectSyncControlStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "project sync controls unavailable"})
		return
	}
	project := strings.TrimSpace(r.PathValue("project"))
	if project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return
	}
	var reason string
	if !enabled {
		var err error
		reason, err = s.parsePausedReason(w, r)
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}

	actor := s.operatorActor(r)
	if err := pcs.SetProjectSyncEnabled(project, enabled, actor, reason); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not update project sync control"})
		return
	}

	action := cloudstore.AuditActionProjectResume
	outcome := cloudstore.AuditOutcomeProjectResumed
	status := "resumed"
	if !enabled {
		action = cloudstore.AuditActionProjectPause
		outcome = cloudstore.AuditOutcomeProjectPaused
		status = "paused"
	}
	if auditor, ok := s.store.(interface {
		InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error
	}); ok {
		var meta map[string]any
		if reason != "" {
			meta = map[string]any{"reason": reason}
		}
		if aerr := auditor.InsertAuditEntry(r.Context(), cloudstore.AuditEntry{
			Contributor: actor,
			Project:     project,
			Action:      action,
			Outcome:     outcome,
			Metadata:    meta,
		}); aerr != nil {
			log.Printf("cloudserver: audit insert failed (%s): %v", action, aerr)
		}
	} else {
		log.Printf("cloudserver: store (%T) does not implement InsertAuditEntry; audit skipped", s.store)
	}

	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]any{
		"project":       project,
		"status":        status,
		"sync_enabled":  enabled,
		"paused_reason": reason,
	})
}
