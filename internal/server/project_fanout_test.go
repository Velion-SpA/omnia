package server

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestResolveProjectFanoutParams covers P5's project-scoping query-param
// resolution (docs/conversational-retrieval-plan.md "P5 — Cross-project
// retrieval") — the ONLY place handleSearch/handleAnswer decide
// fan-out-vs-single-project routing. The "neither param present" case is
// the one the hard back-compat gate depends on: it must return the exact
// zero value (project="", allProjects=false, projects=nil) the SearchFunc/
// AnswerFunc implementation's `req.AllProjects || len(req.Projects) > 0`
// short-circuit checks, or the single-project path would never run
// byte-for-byte unchanged.
func TestResolveProjectFanoutParams(t *testing.T) {
	tests := []struct {
		name            string
		rawQuery        string
		wantProject     string
		wantAllProjects bool
		wantProjects    []string
	}{
		{
			name:            "neither param present",
			rawQuery:        "q=hello",
			wantProject:     "",
			wantAllProjects: false,
			wantProjects:    nil,
		},
		{
			name:            "single project= is the pre-P5 shape, unchanged",
			rawQuery:        "q=hello&project=omnia",
			wantProject:     "omnia",
			wantAllProjects: false,
			wantProjects:    nil,
		},
		{
			name:            "all_projects=1 alone",
			rawQuery:        "q=hello&all_projects=1",
			wantProject:     "",
			wantAllProjects: true,
			wantProjects:    nil,
		},
		{
			name:            "2+ repeated project= alone triggers an explicit-subset fan-out",
			rawQuery:        "q=hello&project=omnia&project=workly",
			wantProject:     "",
			wantAllProjects: false,
			wantProjects:    []string{"omnia", "workly"},
		},
		{
			name:            "3 repeated project= values are all preserved",
			rawQuery:        "q=hello&project=omnia&project=workly&project=vel-voice-assistant",
			wantProject:     "",
			wantAllProjects: false,
			wantProjects:    []string{"omnia", "workly", "vel-voice-assistant"},
		},
		{
			name:            "all_projects=1 wins over repeated project= values (mem_search's own contract)",
			rawQuery:        "q=hello&all_projects=1&project=omnia&project=workly",
			wantProject:     "",
			wantAllProjects: true,
			wantProjects:    nil,
		},
		{
			name:            "all_projects=1 wins over a single project= too",
			rawQuery:        "q=hello&all_projects=1&project=omnia",
			wantProject:     "",
			wantAllProjects: true,
			wantProjects:    nil,
		},
		{
			name:            "all_projects=false explicitly is the same as absent",
			rawQuery:        "q=hello&all_projects=false&project=omnia",
			wantProject:     "omnia",
			wantAllProjects: false,
			wantProjects:    nil,
		},
		{
			name:            "all_projects with an unparseable value falls back to the default (false)",
			rawQuery:        "q=hello&all_projects=notabool&project=omnia&project=workly",
			wantProject:     "",
			wantAllProjects: false,
			wantProjects:    []string{"omnia", "workly"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/search?"+tt.rawQuery, nil)
			gotProject, gotAllProjects, gotProjects := resolveProjectFanoutParams(req)
			if gotProject != tt.wantProject {
				t.Errorf("project = %q, want %q", gotProject, tt.wantProject)
			}
			if gotAllProjects != tt.wantAllProjects {
				t.Errorf("allProjects = %v, want %v", gotAllProjects, tt.wantAllProjects)
			}
			if !reflect.DeepEqual(gotProjects, tt.wantProjects) {
				t.Errorf("projects = %#v, want %#v", gotProjects, tt.wantProjects)
			}
		})
	}
}

// TestResolveProjectFanoutParams_ZeroValueMatchesShortCircuit is a narrower,
// explicit assertion on the exact case the hard back-compat gate leans on:
// a request with no project=/all_projects= params at all must produce
// AllProjects=false and Projects=nil (not an empty-but-non-nil slice),
// because cmd/omnia's buildHTTPSearchFunc/buildHTTPAnswerFunc branch on
// `req.AllProjects || len(req.Projects) > 0` — len(nil) == 0, so this is
// the one shape that guarantees the single-project code path runs.
func TestResolveProjectFanoutParams_ZeroValueMatchesShortCircuit(t *testing.T) {
	req := httptest.NewRequest("GET", "/search?q=hello", nil)
	project, allProjects, projects := resolveProjectFanoutParams(req)

	if project != "" || allProjects != false || len(projects) != 0 {
		t.Fatalf("resolveProjectFanoutParams with no fan-out params = (%q, %v, %v), want the exact zero value the single-project short-circuit checks for",
			project, allProjects, projects)
	}
}
