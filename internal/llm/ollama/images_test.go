package ollama_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/ollama"
)

// TestRejectsImages 画像を黙って落とすとモデルが見ていない画像について答えるため、送信前に拒否する
func TestRejectsImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("provider must not call the API when images are present")
	}))
	defer srv.Close()
	c := ollama.New(ollama.Options{BaseURL: srv.URL})
	req := llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "q", Images: []llm.Image{{MIMEType: "image/png", Data: []byte("x")}}}}}
	if _, err := c.Chat(context.Background(), req); !errors.Is(err, llm.ErrImagesUnsupported) {
		t.Errorf("Chat err = %v", err)
	}
	if _, err := c.Stream(context.Background(), req); !errors.Is(err, llm.ErrImagesUnsupported) {
		t.Errorf("Stream err = %v", err)
	}
}
