package embed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeneratePostsChat(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if len(b) == 0 {
			t.Fatal("empty body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"digest"}}`))
	}))
	defer s.Close()
	got, err := New(s.URL, "test", 0).Generate(context.Background(), "prompt")
	if err != nil || got != "digest" {
		t.Fatalf("%q %v", got, err)
	}
}
