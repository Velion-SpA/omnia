package cloudserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
	"github.com/Velion-SpA/omnia/internal/ui"
)

// Operator-only, read-only Audit page (OBL-05). Surfaces the expanded audit
// trail (token issuance/revocation, login/signup, membership grant/revoke,
// device create/revoke, user disable/enable, admin promote/demote, project
// pause/resume, paused-push rejections) alongside the rest of the Admin
// section, following the exact OBL-13/15 pattern: operator-gated, reuses the
// shared ui.Layout shell, and is registered only when the store supports the
// capability it needs.

// auditLogStore is the read-side capability for the Audit page. Detected via
// type assertion on s.store (the same capability-detection pattern as
// adminDashboardStore / projectSyncControlAdminStore), so the core ChunkStore
// interface is NOT extended. *cloudstore.CloudStore satisfies it — the same
// method already backs the pre-OBL-05 audit surface (paused-push rejections),
// so no new store capability is introduced, only a UI on top of it.
type auditLogStore interface {
	ListAuditEntriesPaginated(ctx context.Context, filter cloudstore.AuditFilter, limit, offset int) ([]cloudstore.DashboardAuditRow, int, error)
}

// Compile-time assertion: the concrete store must satisfy the seam.
var _ auditLogStore = (*cloudstore.CloudStore)(nil)

func (s *CloudServer) auditStore() (auditLogStore, bool) {
	as, ok := s.store.(auditLogStore)
	return as, ok
}

// adminAuditPageSize is the fixed page size for the Audit page's pagination.
const adminAuditPageSize = 50

type adminAuditRow struct {
	OccurredAt  string
	Contributor string
	Project     string
	Action      string
	Outcome     string
	ReasonCode  string
}

type adminAuditView struct {
	Props       ui.LayoutProps
	Rows        []adminAuditRow
	Total       int
	Page        int
	PageCount   int
	Contributor string
	Project     string
	Outcome     string
}

// handleAdminAuditPage renders GET /admin/audit. Operator-only (requireOperator
// re-checks on every call, matching the rest of the Admin section — the UI is
// never trusted). Supports optional filtering via ?contributor=&project=&outcome=
// and page-based pagination via ?page=.
func (s *CloudServer) handleAdminAuditPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.auditStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "audit log unavailable"})
		return
	}
	q := r.URL.Query()
	filter := cloudstore.AuditFilter{
		Contributor: strings.TrimSpace(q.Get("contributor")),
		Project:     strings.TrimSpace(q.Get("project")),
		Outcome:     strings.TrimSpace(q.Get("outcome")),
	}
	page := 1
	if p, err := strconv.Atoi(strings.TrimSpace(q.Get("page"))); err == nil && p > 0 {
		page = p
	}
	offset := (page - 1) * adminAuditPageSize
	rows, total, err := as.ListAuditEntriesPaginated(r.Context(), filter, adminAuditPageSize, offset)
	if err != nil {
		http.Error(w, "could not list audit entries", http.StatusInternalServerError)
		return
	}
	pageCount := (total + adminAuditPageSize - 1) / adminAuditPageSize
	if pageCount < 1 {
		pageCount = 1
	}
	view := adminAuditView{
		Props:       s.adminLayoutProps("Admin · Audit", "audit"),
		Rows:        toAdminAuditRows(rows),
		Total:       total,
		Page:        page,
		PageCount:   pageCount,
		Contributor: filter.Contributor,
		Project:     filter.Project,
		Outcome:     filter.Outcome,
	}
	if err := adminAuditPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func toAdminAuditRows(rows []cloudstore.DashboardAuditRow) []adminAuditRow {
	out := make([]adminAuditRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, adminAuditRow{
			OccurredAt:  row.OccurredAt,
			Contributor: row.Contributor,
			Project:     row.Project,
			Action:      row.Action,
			Outcome:     row.Outcome,
			ReasonCode:  row.ReasonCode,
		})
	}
	return out
}

// adminAuditPagePath builds a pagination link preserving the current filters.
func adminAuditPagePath(view adminAuditView, page int) string {
	q := url.Values{}
	if view.Contributor != "" {
		q.Set("contributor", view.Contributor)
	}
	if view.Project != "" {
		q.Set("project", view.Project)
	}
	if view.Outcome != "" {
		q.Set("outcome", view.Outcome)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/admin/audit"
	}
	return "/admin/audit?" + q.Encode()
}

func formatAuditTotal(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

func formatAuditPage(n int) string {
	return strconv.Itoa(n)
}
