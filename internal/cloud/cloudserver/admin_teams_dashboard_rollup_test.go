package cloudserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// ─── Pure function tests: projectRollupStats ─────────────────────────────────

func TestProjectRollupStats_NoChildrenReturnsOwnUnchanged(t *testing.T) {
	own := cloudstore.ProjectChunkStats{MemoryCount: 7, SourceCount: 1, LastActivity: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	got := projectRollupStats(own, nil)

	if got != own {
		t.Fatalf("expected rollup with no children to equal own stats unchanged, got %+v, want %+v", got, own)
	}
}

func TestProjectRollupStats_SumsMemoryAndSourceAcrossChildren(t *testing.T) {
	own := cloudstore.ProjectChunkStats{MemoryCount: 10, SourceCount: 2}
	children := []cloudstore.ProjectChunkStats{
		{MemoryCount: 55, SourceCount: 6},
		{MemoryCount: 33, SourceCount: 4},
	}

	got := projectRollupStats(own, children)

	if got.MemoryCount != 98 {
		t.Errorf("MemoryCount = %d, want 98 (10+55+33)", got.MemoryCount)
	}
	if got.SourceCount != 12 {
		t.Errorf("SourceCount = %d, want 12 (2+6+4)", got.SourceCount)
	}
}

func TestProjectRollupStats_LastActivityIsMaxAcrossOwnAndChildren(t *testing.T) {
	own := cloudstore.ProjectChunkStats{LastActivity: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	newestChild := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	children := []cloudstore.ProjectChunkStats{
		{LastActivity: newestChild},
		{LastActivity: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)},
	}

	got := projectRollupStats(own, children)

	if !got.LastActivity.Equal(newestChild) {
		t.Fatalf("LastActivity = %v, want max (newest child) %v", got.LastActivity, newestChild)
	}
}

func TestProjectRollupStats_OwnLastActivityWinsWhenNewerThanChildren(t *testing.T) {
	ownNewest := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	own := cloudstore.ProjectChunkStats{LastActivity: ownNewest}
	children := []cloudstore.ProjectChunkStats{{LastActivity: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}}

	got := projectRollupStats(own, children)

	if !got.LastActivity.Equal(ownNewest) {
		t.Fatalf("LastActivity = %v, want own (newest) %v", got.LastActivity, ownNewest)
	}
}

// ─── Pure function tests: buildProjectGroups ─────────────────────────────────

func TestBuildProjectGroups_NestsChildrenUnderParentInOrder(t *testing.T) {
	// "workly-a-standalone" is deliberately alphabetically BETWEEN "workly"
	// and "workly-marketing" ('-'+'a' < '-'+'m'), so a naive flat render
	// would interleave it between the parent and its first child — proving
	// grouping actually reorders for display, not just adds a wrapper.
	rows := []adminProjectRow{
		{Project: "workly"},
		{Project: "workly-a-standalone"},
		{Project: "workly-marketing", ParentProject: "workly"},
		{Project: "workly-videos", ParentProject: "workly"},
	}
	childrenOf := map[string][]string{
		"workly": {"workly-marketing", "workly-videos"},
	}

	groups := buildProjectGroups(rows, childrenOf)

	if len(groups) != 2 {
		t.Fatalf("expected 2 top-level groups (workly, workly-a-standalone), got %d: %+v", len(groups), groups)
	}
	if groups[0].Row.Project != "workly" {
		t.Fatalf("expected first group to be workly, got %q", groups[0].Row.Project)
	}
	if len(groups[0].Children) != 2 || groups[0].Children[0].Project != "workly-marketing" || groups[0].Children[1].Project != "workly-videos" {
		t.Fatalf("expected workly's group to nest both children in order, got %+v", groups[0].Children)
	}
	if groups[1].Row.Project != "workly-a-standalone" || len(groups[1].Children) != 0 {
		t.Fatalf("expected workly-a-standalone as its own standalone top-level entry, got %+v", groups[1])
	}
}

func TestBuildProjectGroups_StandaloneProjectsHaveNoChildren(t *testing.T) {
	rows := []adminProjectRow{{Project: "solo"}}

	groups := buildProjectGroups(rows, map[string][]string{})

	if len(groups) != 1 || groups[0].Row.Project != "solo" || len(groups[0].Children) != 0 {
		t.Fatalf("expected one standalone group with no children, got %+v", groups)
	}
}

func TestBuildProjectGroups_OrphanedChildWithUnknownParentRendersStandalone(t *testing.T) {
	// The child's ParentProject ("ghost") never appears as its own row (e.g.
	// the parent has no cloud_project_meta) — the child must still render,
	// never be silently dropped.
	rows := []adminProjectRow{
		{Project: "workly-marketing", ParentProject: "ghost"},
	}

	groups := buildProjectGroups(rows, map[string][]string{})

	if len(groups) != 1 || groups[0].Row.Project != "workly-marketing" {
		t.Fatalf("expected the orphaned child to render standalone, got %+v", groups)
	}
}

// ─── HTTP-level tests: handler + template wiring ─────────────────────────────

// fakeProjectRollupStore combines the Slice 4b stats capability
// (fakeProjectAdminStatsStore) and the Slice 5a linking capability
// (fakeProjectLinksStore) on ONE fake — Slice 5b's roll-up needs both maps
// simultaneously (ListProjectChunkStats + ListProjectParents), which neither
// existing single-capability fake alone provides.
type fakeProjectRollupStore struct {
	*fakeTeamsStore
	chunkStats map[string]cloudstore.ProjectChunkStats
	access     map[string][]cloudstore.ProjectAccessRow
	parents    map[string]string
}

func newFakeProjectRollupStore() *fakeProjectRollupStore {
	return &fakeProjectRollupStore{
		fakeTeamsStore: newFakeTeamsStore(),
		chunkStats:     map[string]cloudstore.ProjectChunkStats{},
		access:         map[string][]cloudstore.ProjectAccessRow{},
		parents:        map[string]string{},
	}
}

func (s *fakeProjectRollupStore) ListProjectChunkStats(context.Context) (map[string]cloudstore.ProjectChunkStats, error) {
	return s.chunkStats, nil
}

func (s *fakeProjectRollupStore) ListAccountAccessForProject(_ context.Context, project string) ([]cloudstore.ProjectAccessRow, error) {
	return s.access[project], nil
}

func (s *fakeProjectRollupStore) SetProjectParent(_ context.Context, child, parent string) error {
	s.parents[child] = parent
	if s.projectMeta[child] == nil {
		s.projectMeta[child] = &cloudstore.ProjectMeta{Project: child, Kind: "work"}
	}
	return nil
}

func (s *fakeProjectRollupStore) ClearProjectParent(_ context.Context, child string) error {
	delete(s.parents, child)
	return nil
}

func (s *fakeProjectRollupStore) ListProjectParents(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.parents))
	for k, v := range s.parents {
		out[k] = v
	}
	return out, nil
}

var _ projectAdminStatsStore = (*fakeProjectRollupStore)(nil)
var _ projectLinksAdminStore = (*fakeProjectRollupStore)(nil)

func newProjectRollupTestServer(t *testing.T) (*CloudServer, *fakeProjectRollupStore, *cloudauth.Service) {
	t.Helper()
	store := newFakeProjectRollupStore()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	return srv, store, authSvc
}

func TestHandleAdminProjectsPage_ParentCardShowsRolledUpStats(t *testing.T) {
	srv, store, authSvc := newProjectRollupTestServer(t)
	store.projectMeta["workly"] = &cloudstore.ProjectMeta{Project: "workly", Kind: "work"}
	store.projectMeta["workly-marketing"] = &cloudstore.ProjectMeta{Project: "workly-marketing", Kind: "work"}
	store.projectMeta["workly-videos"] = &cloudstore.ProjectMeta{Project: "workly-videos", Kind: "work"}
	store.parents["workly-marketing"] = "workly"
	store.parents["workly-videos"] = "workly"
	store.chunkStats["workly"] = cloudstore.ProjectChunkStats{MemoryCount: 10, SourceCount: 2}
	store.chunkStats["workly-marketing"] = cloudstore.ProjectChunkStats{MemoryCount: 55, SourceCount: 6}
	store.chunkStats["workly-videos"] = cloudstore.ProjectChunkStats{MemoryCount: 33, SourceCount: 4}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{">98<", ">12<"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected parent card to show rolled-up stat %q, got: %s", want, body)
		}
	}
	// Children must still show their OWN individual stats, not the rollup.
	for _, want := range []string{">55<", ">33<"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected a child card to show its own (unrolled) stat %q, got: %s", want, body)
		}
	}
}

func TestHandleAdminProjectsPage_StandaloneProjectStatsUnchanged(t *testing.T) {
	srv, store, authSvc := newProjectRollupTestServer(t)
	store.projectMeta["solo"] = &cloudstore.ProjectMeta{Project: "solo", Kind: "work"}
	store.chunkStats["solo"] = cloudstore.ProjectChunkStats{MemoryCount: 7, SourceCount: 1}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">7<") {
		t.Errorf("expected solo project's own memory count (7) unchanged, got: %s", body)
	}
}

func TestHandleAdminProjectsPage_GroupingRendersChildrenNested(t *testing.T) {
	srv, store, authSvc := newProjectRollupTestServer(t)
	store.projectMeta["workly"] = &cloudstore.ProjectMeta{Project: "workly", Kind: "work"}
	store.projectMeta["workly-marketing"] = &cloudstore.ProjectMeta{Project: "workly-marketing", Kind: "work"}
	store.parents["workly-marketing"] = "workly"

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="projgroup"`) {
		t.Errorf("expected a projgroup wrapper for the parent+child group, got: %s", body)
	}
	if !strings.Contains(body, `class="projgroup-children"`) {
		t.Errorf("expected a projgroup-children wrapper nesting the child card, got: %s", body)
	}
}
