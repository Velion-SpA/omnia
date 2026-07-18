package jira

import (
	"testing"
	"time"
)

// TestBuildJQL_EscapesProjectKeyQuotesAndBackslashes locks in the current
// safe %q-based escaping of buildJQL's project key against regression. A
// project key containing a double-quote or backslash must never be able to
// break out of the quoted JQL string literal (JQL injection).
func TestBuildJQL_EscapesProjectKeyQuotesAndBackslashes(t *testing.T) {
	tests := []struct {
		name       string
		projectKey string
		want       string
	}{
		{
			name:       "plain project key",
			projectKey: "ENG",
			want:       `project = "ENG" ORDER BY updated ASC`,
		},
		{
			name:       "embedded double quote is escaped",
			projectKey: `EN"G`,
			want:       `project = "EN\"G" ORDER BY updated ASC`,
		},
		{
			name:       "embedded backslash is escaped",
			projectKey: `EN\G`,
			want:       `project = "EN\\G" ORDER BY updated ASC`,
		},
		{
			name:       "embedded quote and backslash together",
			projectKey: `EN\"G`,
			want:       `project = "EN\\\"G" ORDER BY updated ASC`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildJQL(tt.projectKey, time.Time{})
			if got != tt.want {
				t.Errorf("buildJQL(%q, zero) = %q, want %q", tt.projectKey, got, tt.want)
			}
		})
	}
}

// TestBuildJQL_CursorValueIsFixedLayoutNoInjectionRisk locks in that the
// "updated >=" cursor value comes from a fixed time.Format layout
// (jqlTimeLayout), never from raw/attacker-controlled text — a
// time.Time-derived string can't contain a quote or backslash, so no
// escaping is needed there (unlike the project key, which IS a raw
// string). This test pins the exact rendered clause so any future change
// to how the cursor is formatted/escaped is caught.
func TestBuildJQL_CursorValueIsFixedLayoutNoInjectionRisk(t *testing.T) {
	since := time.Date(2024, 1, 5, 9, 30, 0, 0, time.UTC)
	got := buildJQL("ENG", since)
	want := `project = "ENG" AND updated >= "2024-01-05 09:30" ORDER BY updated ASC`
	if got != want {
		t.Errorf("buildJQL(ENG, 2024-01-05 09:30) = %q, want %q", got, want)
	}
}
