package chunkcodec

import (
	"errors"
	"strings"
	"testing"
)

// TestCanonicalizeTagsClientPayloadErrors locks that payload-validation
// failures carry ErrInvalidPayload out of CanonicalizeForProject, so callers
// can answer 400 instead of 500.
//
// The prompt case is the live one: project "umbral" pushed an empty-content
// prompt upsert and the cloud answered 500, which the client treats as a
// retryable transport fault and retries forever.
func TestCanonicalizeTagsClientPayloadErrors(t *testing.T) {
	tests := []struct {
		name    string
		chunk   string
		wantErr string
	}{
		{
			name:    "prompt upsert with empty content",
			chunk:   `{"mutations":[{"entity":"prompt","entity_key":"prompt-a7cb56e905751772","op":"upsert","payload":"{\"sync_id\":\"prompt-a7cb56e905751772\",\"session_id\":\"s-1\",\"content\":\"\"}"}]}`,
			wantErr: "prompt payload content is required for upsert",
		},
		{
			name:    "prompt upsert with whitespace-only content",
			chunk:   `{"mutations":[{"entity":"prompt","entity_key":"prompt-a7cb56e905751772","op":"upsert","payload":"{\"sync_id\":\"prompt-a7cb56e905751772\",\"session_id\":\"s-1\",\"content\":\"   \"}"}]}`,
			wantErr: "prompt payload content is required for upsert",
		},
		{
			name:    "observation upsert missing title",
			chunk:   `{"mutations":[{"entity":"observation","entity_key":"obs-1","op":"upsert","payload":"{\"sync_id\":\"obs-1\",\"session_id\":\"s-1\",\"type\":\"decision\",\"content\":\"c\",\"scope\":\"project\"}"}]}`,
			wantErr: "observation payload title is required for upsert",
		},
		{
			name:    "undecodable payload",
			chunk:   `{"mutations":[{"entity":"prompt","entity_key":"p-1","op":"upsert","payload":"not json"}]}`,
			wantErr: "decode mutation payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanonicalizeForProject([]byte(tt.chunk), "umbral")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrInvalidPayload) {
				t.Errorf("error is not tagged ErrInvalidPayload, so the caller cannot answer 400: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q — the operator loses the reason", err, tt.wantErr)
			}
		})
	}
}

// TestCanonicalizeValidPayloadStillPasses guards against the sentinel work
// rejecting good input.
func TestCanonicalizeValidPayloadStillPasses(t *testing.T) {
	chunk := `{"mutations":[{"entity":"prompt","entity_key":"p-1","op":"upsert","payload":"{\"sync_id\":\"p-1\",\"session_id\":\"s-1\",\"content\":\"a real prompt\"}"}]}`
	if _, err := CanonicalizeForProject([]byte(chunk), "umbral"); err != nil {
		t.Fatalf("valid prompt payload rejected: %v", err)
	}
}
