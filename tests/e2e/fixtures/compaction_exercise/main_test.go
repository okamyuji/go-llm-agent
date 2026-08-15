package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseCompactCounts_ExtractsBeforeAndAfter(t *testing.T) {
	out := "前置き\n[compact] 会話履歴を圧縮しました (4件 -> 2件)\n続き\n"
	before, after, ok := parseCompactCounts(out)
	if !ok || before != 4 || after != 2 {
		t.Fatalf("before=%d after=%d ok=%v", before, after, ok)
	}
}

func TestParseCompactCounts_MissingLineReturnsNotOK(t *testing.T) {
	if _, _, ok := parseCompactCounts("[compact] 圧縮対象がありません\n"); ok {
		t.Fatal("圧縮行が無ければ ok=false")
	}
}

func TestParseCompactCounts_MalformedLineReturnsNotOK(t *testing.T) {
	if _, _, ok := parseCompactCounts("[compact] 会話履歴を圧縮しました (xx)"); ok {
		t.Fatal("解析できない行は ok=false")
	}
}

func TestHasConsecutiveUser(t *testing.T) {
	tests := []struct {
		name string
		msgs []reqMsg
		want bool
	}{
		{"空", nil, false},
		{"単独", []reqMsg{{Role: "user"}}, false},
		{"交互", []reqMsg{{Role: "user"}, {Role: "assistant"}, {Role: "user"}}, false},
		{"連続", []reqMsg{{Role: "user"}, {Role: "user"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasConsecutiveUser(tt.msgs); got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestHasSummaryUserMessage(t *testing.T) {
	ok := []reqMsg{{Role: "user", Content: summaryMark + "\n\n" + summaryText + "\n\n本文"}}
	if !hasSummaryUserMessage(ok) {
		t.Fatal("要約入り user メッセージを検出できていない")
	}
	ngRole := []reqMsg{{Role: "assistant", Content: summaryMark + summaryText}}
	if hasSummaryUserMessage(ngRole) {
		t.Fatal("assistant ロールは対象外")
	}
	if hasSummaryUserMessage([]reqMsg{{Role: "user", Content: "本文"}}) {
		t.Fatal("要約が無ければ false")
	}
}

func TestStub_NextInputTokens_UsesLastValueBeyondRange(t *testing.T) {
	s := &stub{usages: []int{10, 60}}
	for _, want := range []int{10, 60, 60} {
		if got := s.nextInputTokens(nil); got != want {
			t.Fatalf("got=%d want=%d", got, want)
		}
	}
	if _, streams := s.counts(); streams != 3 {
		t.Fatalf("streamCalls=%d want 3", streams)
	}
}

func TestStub_NextInputTokens_EmptyUsagesReturnsOne(t *testing.T) {
	s := &stub{}
	if got := s.nextInputTokens(nil); got != 1 {
		t.Fatalf("got=%d want 1", got)
	}
}

func TestStub_LastStreamMessages_EmptyReturnsNil(t *testing.T) {
	s := &stub{}
	if got := s.lastStreamMessages(); got != nil {
		t.Fatalf("got=%v want nil", got)
	}
}

func TestStub_LastStreamMessages_ReturnsMostRecent(t *testing.T) {
	s := &stub{}
	s.nextInputTokens([]reqMsg{{Role: "user", Content: "1"}})
	s.nextInputTokens([]reqMsg{{Role: "user", Content: "2"}})
	got := s.lastStreamMessages()
	if len(got) != 1 || got[0].Content != "2" {
		t.Fatalf("got=%v", got)
	}
}

func TestRun_AllKeysEmitted(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), &out); err != nil {
		t.Fatalf("run err=%v", err)
	}
	for _, key := range []string{
		"compaction_auto=true",
		"compaction_manual=true",
		"compaction_no_consecutive_user=true",
		"compaction_noop_reported=true",
	} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("missing %q in %q", key, out.String())
		}
	}
}
