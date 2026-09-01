package cliui

import (
	"strings"
	"testing"
)

// runEscSequence intro 以降のバイト列を流して passThroughEscSequence を実行し、
// 次の行編集へ引き継がれた pending と pasteActive を返す
func runEscSequence(t *testing.T, feed string) ([]byte, bool) {
	t.Helper()
	pump := newBytePump(strings.NewReader(feed))
	r := &REPL{}
	st := &turnState{}
	r.passThroughEscSequence(pump, '[', st)
	return pump.pending, st.pasteActive
}

// TestPassThroughEscSequence_Terminators 終端文字 ([a-zA-Z~]) の境界値で
// 列の収集が止まり、後続バイトを巻き込まないことを検証する
func TestPassThroughEscSequence_Terminators(t *testing.T) {
	cases := []struct {
		name string
		feed string
		want string
	}{
		{"小文字下限a", "2aXY", "\x1b[2a"},
		{"小文字上限z", "2zXY", "\x1b[2z"},
		{"大文字下限A", "1;3AXY", "\x1b[1;3A"},
		{"大文字上限Z", "1;3ZXY", "\x1b[1;3Z"},
		{"チルダ", "3~XY", "\x1b[3~"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pending, _ := runEscSequence(t, tc.feed)
			if string(pending) != tc.want {
				t.Fatalf("pending=%q, want %q", pending, tc.want)
			}
		})
	}
}

// TestPassThroughEscSequence_CapsAtMaxLen 終端が現れない列は 8 バイトで
// 打ち切って引き継ぐ (ゴミ入力でイベントループを停滞させない)
func TestPassThroughEscSequence_CapsAtMaxLen(t *testing.T) {
	pending, _ := runEscSequence(t, "0000000000")
	if len(pending) != 8 {
		t.Fatalf("len(pending)=%d, want 8: %q", len(pending), pending)
	}
}

// TestPassThroughEscSequence_PasteMarkers ペーストマーカーで pasteActive が
// 開始・終了に切り替わる
func TestPassThroughEscSequence_PasteMarkers(t *testing.T) {
	if _, active := runEscSequence(t, "200~"); !active {
		t.Fatal("pasteStart で pasteActive=true になる期待")
	}
	pump := newBytePump(strings.NewReader("201~"))
	r := &REPL{}
	st := &turnState{pasteActive: true}
	r.passThroughEscSequence(pump, '[', st)
	if st.pasteActive {
		t.Fatal("pasteEnd で pasteActive=false になる期待")
	}
}
