package dashboard

import (
	"net/http"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// filterRowsByScope returns only the rows whose project the principal may view.
// A scope-all principal (operator) receives the slice unchanged. This is the
// last line of defence for list endpoints that return rows across many projects.
func filterRowsByScope[T any](p Principal, rows []T, projectOf func(T) string) []T {
	if p.ScopeAll() {
		return rows
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		if p.CanView(projectOf(row)) {
			out = append(out, row)
		}
	}
	return out
}

func projectOfRow(r cloudstore.DashboardProjectRow) string      { return r.Project }
func projectOfObservation(r cloudstore.DashboardObservationRow) string { return r.Project }
func projectOfSession(r cloudstore.DashboardSessionRow) string   { return r.Project }
func projectOfPrompt(r cloudstore.DashboardPromptRow) string     { return r.Project }

// denyIfHidden gates a project-scoped route. When the principal may not view the
// given project it writes a "Not Found" response (HTMX-aware) and returns true.
// Returning Not Found rather than Forbidden avoids confirming the existence of
// projects an account has no access to.
func (h *handlers) denyIfHidden(w http.ResponseWriter, r *http.Request, p Principal, project string) bool {
	if p.CanView(project) {
		return false
	}
	if isHTMXRequest(r) {
		renderComponentStatus(w, r, http.StatusNotFound, EmptyState("Project Not Found", "No replicated dashboard data exists for that project."))
		return true
	}
	renderComponentStatus(w, r, http.StatusNotFound, Layout("Not Found", p.DisplayName(), "projects", p.IsAdmin(),
		EmptyState("Project Not Found", "No replicated dashboard data exists for that project.")))
	return true
}
