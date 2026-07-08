package cloudserver

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
)

// OBL-05: shared best-effort audit helper for the expanded security-event
// surface (login, signup, membership grant/revoke, device create/revoke,
// token revoke, user disable/enable, admin promote/demote). It centralizes
// the same structural-typing + "log and continue on failure" pattern already
// established by OBL-04 (project_sync_admin.go) and REQ-404/407
// (mutations.go / cloudserver.go), so every NEW call site does not repeat the
// inline type assertion. It intentionally never blocks or fails the caller's
// action — only credential ISSUANCE (tokens.go IssueManagedToken) is atomic
// with its audit row; everything routed through emitAudit is best-effort.
func (s *CloudServer) emitAudit(r *http.Request, entry cloudstore.AuditEntry) {
	auditor, ok := s.store.(interface {
		InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error
	})
	if !ok {
		log.Printf("cloudserver: store (%T) does not implement InsertAuditEntry; audit skipped (%s)", s.store, entry.Action)
		return
	}
	if aerr := auditor.InsertAuditEntry(r.Context(), entry); aerr != nil {
		log.Printf("cloudserver: audit insert failed (%s): %v", entry.Action, aerr)
	}
}

// clientIP returns a best-effort caller IP for audit metadata: the first hop
// in X-Forwarded-For when present (reverse-proxied deployments), otherwise the
// direct RemoteAddr host. Empty when neither is available. This is metadata
// only — never used for authorization — so a spoofable header is acceptable.
func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if first := strings.TrimSpace(strings.Split(fwd, ",")[0]); first != "" {
			return first
		}
	}
	host := strings.TrimSpace(r.RemoteAddr)
	if idx := strings.LastIndex(host, ":"); idx > 0 && !strings.HasSuffix(host, "]") {
		host = host[:idx]
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}
