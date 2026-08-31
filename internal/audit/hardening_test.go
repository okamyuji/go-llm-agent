package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestStreamCloseBeforeCompletionRecordsError(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	p := &scriptedProvider{events: []llm.StreamEvent{{DeltaText: "partial"}, {DeltaText: " rest"}}}
	st, err := WrapProvider(p, e).Stream(WithSessionID(context.Background(), "s"), llm.ChatRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Recv(); !ok {
		t.Fatal("first Recv must succeed")
	}
	_ = st.Close() // 終端前に閉じる
	_ = e.Shutdown(context.Background())
	var found bool
	for _, ev := range readAllEvents(t, dir) {
		if ev.Kind != KindLLMResponse {
			continue
		}
		found = true
		var pl LLMResponsePayload
		_ = json.Unmarshal(ev.Payload, &pl)
		if pl.Error != errStreamClosedEarly.Error() || pl.Content != "partial" {
			t.Fatalf("early close must be recorded as incomplete: %s", ev.Payload)
		}
	}
	if !found {
		t.Fatal("llm_response not recorded")
	}
}

func TestStreamCloseAfterCompletionIsSuccess(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	p := &scriptedProvider{events: []llm.StreamEvent{{DeltaText: "done"}}}
	st, _ := WrapProvider(p, e).Stream(WithSessionID(context.Background(), "s"), llm.ChatRequest{Model: "m"})
	for {
		if _, ok := st.Recv(); !ok {
			break
		}
	}
	_ = st.Close()
	_ = e.Shutdown(context.Background())
	for _, ev := range readAllEvents(t, dir) {
		if ev.Kind == KindLLMResponse {
			var pl LLMResponsePayload
			_ = json.Unmarshal(ev.Payload, &pl)
			if pl.Error != "" {
				t.Fatalf("completed stream must not carry an error: %s", ev.Payload)
			}
		}
	}
}

func TestLLMResponseRedactsErrorText(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p", Redactor: upperRedactor{}})
	e.LLMResponse(WithSessionID(context.Background(), "s"), "prov", "m", "", nil, "", errors.New("body SECRET leaked"))
	_ = e.Shutdown(context.Background())
	evs := readAllEvents(t, dir)
	if len(evs) != 1 {
		t.Fatalf("events=%d", len(evs))
	}
	var pl LLMResponsePayload
	_ = json.Unmarshal(evs[0].Payload, &pl)
	if pl.Error != "body [R] leaked" {
		t.Fatalf("error text not redacted: %q", pl.Error)
	}
}

func TestValidateIggyURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://iggy.example.com:3000", true},
		{"http://127.0.0.1:3000", true},
		{"http://localhost:3000", true},
		{"http://[::1]:3000", true},
		{"http://10.0.0.1:3000", false},
		{"http://iggy.example.com", false},
		{"ftp://127.0.0.1", false},
		{"://bad", false},
	}
	for _, c := range cases {
		err := ValidateIggyURL(c.url)
		if (err == nil) != c.ok {
			t.Errorf("%s: ok=%v err=%v", c.url, c.ok, err)
		}
	}
}

func TestIggyClientDoesNotFollowRedirects(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(200) }))
	defer target.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	c := newIggyClient(redirecting.URL, "pat", "s", "0")
	c.token = "tok"
	code, _, err := c.do(context.Background(), http.MethodGet, "/streams/s", nil)
	if err != nil || code != http.StatusTemporaryRedirect || hits != 0 {
		t.Fatalf("redirect must not be followed: code=%d hits=%d err=%v", code, hits, err)
	}
}

func TestShutdownKeepsRunLockWhileDrainOutlivesDeadline(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /personal-access-tokens/login", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": map[string]any{"token": "tok"}})
	})
	mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /streams/{s}/topics/{t}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /streams/{s}/topics/{t}/messages", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(201)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := NewEmitter(Options{WALDir: dir, IggyURL: srv.URL, PAT: "p"})
	e.Usage(WithSessionID(context.Background(), "s"), "prov", "m", llm.Usage{InputTokens: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := e.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown must report the deadline: %v", err)
	}
	if _, ok := tryLockRun(dir, e.RunID()); ok {
		t.Fatal("run lock must stay held while the sender is still draining")
	}
	<-e.sender.done
	_ = e.lock.Close()
}
