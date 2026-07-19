package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestConfirmDialog_RendersTitleMessageAndActions guards the reusable inline
// confirm card introduced in the Command Center v2 foundation slice: it must
// render the title, message, note, a cancel trigger targeting the caller's
// hx-target, and one button per action wired to the right htmx verb.
func TestConfirmDialog_RendersTitleMessageAndActions(t *testing.T) {
	props := ConfirmDialogProps{
		Title:     "Delete project?",
		Message:   "trackly",
		Note:      "This cannot be undone.",
		CancelURL: "/admin/projects/trackly/cancel",
		Target:    "#project-row-trackly",
		Actions: []ConfirmAction{
			{Label: "Delete", Method: "delete", URL: "/admin/projects/trackly", Danger: true, Confirm: "Really delete trackly?"},
		},
	}

	var buf bytes.Buffer
	if err := ConfirmDialog(props).Render(context.Background(), &buf); err != nil {
		t.Fatalf("ConfirmDialog render failed: %v", err)
	}
	html := buf.String()

	for _, want := range []string{
		"Delete project?",
		"trackly",
		"This cannot be undone.",
		`hx-get="/admin/projects/trackly/cancel"`,
		`hx-target="#project-row-trackly"`,
		`hx-delete="/admin/projects/trackly"`,
		`hx-confirm="Really delete trackly?"`,
		"pill-btn-danger",
		"Delete",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected rendered ConfirmDialog to contain %q, got:\n%s", want, html)
		}
	}
}

// TestConfirmDialog_OmitsConfirmAttrWhenEmpty guards against htmx receiving an
// empty hx-confirm attribute (which would pop a blank native confirm() box)
// for actions that don't need one.
func TestConfirmDialog_OmitsConfirmAttrWhenEmpty(t *testing.T) {
	props := ConfirmDialogProps{
		Title:     "Revoke access?",
		CancelURL: "/admin/access/cancel",
		Target:    "#access-row-1",
		Actions: []ConfirmAction{
			{Label: "Revoke", Method: "delete", URL: "/admin/memberships/1"},
		},
	}

	var buf bytes.Buffer
	if err := ConfirmDialog(props).Render(context.Background(), &buf); err != nil {
		t.Fatalf("ConfirmDialog render failed: %v", err)
	}
	if strings.Contains(buf.String(), "hx-confirm") {
		t.Fatalf("expected no hx-confirm attribute when Confirm is empty, got:\n%s", buf.String())
	}
}
