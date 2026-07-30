package mcp

import (
	"context"
	"strings"
	"testing"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestHandleBlameOutsideGitRepoReturnsEmptyResult(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleBlame(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"file": t.TempDir() + "/outside.go", "line": float64(1)}}})
	if err != nil {
		t.Fatalf("handleBlame: %v", err)
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "no repo context") || !strings.Contains(text, "hits") {
		t.Fatalf("unexpected result: %s", text)
	}
}
