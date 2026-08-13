package lineedit

import "strings"

// 検索キーは surrogate 領域に置く。isPrintable が偽を返し、行へ混入しない。
const (
	keyReverseSearch = 0xd830 + iota // ^R
	keyAbortSearch                   // ^G
)

// searchState は Ctrl-R 後方インクリメンタル検索の状態。
type searchState struct {
	active bool
	query  []rune
	index  int    // History.At へ渡す一致インデックス。未一致は -1
	failed bool   // 直近の検索が一致しなかった
	line   []rune // 検索開始時の t.line
	pos    int    // 検索開始時の t.pos
}

// displayPrompt 画面に描くプロンプトを返す。プロンプトを参照する箇所は
// 例外なくこれを使う。検索中はクエリを含むため幅も変わる。
func (t *Terminal) displayPrompt() []rune {
	if !t.search.active {
		return t.prompt
	}
	prefix := "(reverse-i-search)"
	if t.search.failed {
		prefix = "(failed reverse-i-search)"
	}
	return []rune(prefix + "'" + string(t.search.query) + "': ")
}

// searchBackward start 以降 (より過去方向) で最初に query を含む履歴を返す。
// 一致は substring。正規化 (大文字小文字・全半角・NFC/NFD) は行わない。
func (t *Terminal) searchBackward(start int) (idx int, entry string, ok bool) {
	for i := start; i < t.History.Len(); i++ {
		e, ok := t.historyAt(i)
		if !ok {
			break
		}
		if strings.Contains(e, string(t.search.query)) {
			return i, e, true
		}
	}
	return -1, "", false
}

// startSearch 現在の行を退避して検索モードへ入る。空クエリでは履歴を走査しない。
func (t *Terminal) startSearch() {
	t.search = searchState{
		active: true,
		index:  -1,
		line:   append([]rune(nil), t.line...),
		pos:    t.pos,
	}
	t.repaintSearch(nil)
}

// repaintSearch 候補行を描き直す。クエリ変更でプロンプト長が変わるため、
// setLine ではなく全消去と再描画で行う。
func (t *Terminal) repaintSearch(candidate []rune) {
	t.line = candidate
	t.pos = len(candidate)
	t.clearAndRepaintLinePlusNPrevious(t.maxLine)
}

// applySearchResult 検索結果を候補として反映する。一致しない場合は候補を保持する。
func (t *Terminal) applySearchResult(idx int, entry string, ok bool) {
	if !ok {
		t.search.failed = true
		t.repaintSearch(t.line)
		return
	}
	t.search.index = idx
	t.search.failed = false
	t.repaintSearch([]rune(entry))
}

// endSearch 検索を終了する。abort なら検索前の行とカーソルを復元し、
// そうでなければ候補行を維持する。いずれも通常プロンプトで再描画してから戻る。
// 再描画を省略すると、検索プロンプト基準の桁位置のまま通常処理へ委譲され、
// 以後の moveCursorToPos が実画面とずれる。
func (t *Terminal) endSearch(abort bool) {
	t.search.active = false
	if abort {
		t.line = t.search.line
		t.pos = t.search.pos
	}
	t.historyIndex = -1
	t.historyPending = ""
	t.clearAndRepaintLinePlusNPrevious(t.maxLine)
}

// endSearchIfActive 検索中であれば候補を確定して終了する (paste 開始時に使う)。
func (t *Terminal) endSearchIfActive() {
	if t.search.active {
		t.endSearch(false)
	}
}

// searchNext より過去方向の次の一致へ進む。
func (t *Terminal) searchNext() {
	t.applySearchResult(t.searchBackward(t.search.index + 1))
}

// searchAppend クエリへ 1 rune 追加して再検索する。現在の候補も再一致対象とする。
func (t *Terminal) searchAppend(key rune) {
	t.search.query = append(t.search.query, key)
	start := t.search.index
	if start < 0 {
		start = 0
	}
	t.applySearchResult(t.searchBackward(start))
}

// searchBackspace クエリ末尾を 1 rune 削って先頭から再検索する。
func (t *Terminal) searchBackspace() {
	if len(t.search.query) == 0 {
		return
	}
	t.search.query = t.search.query[:len(t.search.query)-1]
	t.applySearchResult(t.searchBackward(0))
}

// handleSearchKey 検索中のキーを処理する。検索を終了させるキーは、
// 通常プロンプトでの再描画を済ませてから通常処理へ 1 回だけ委譲する。
func (t *Terminal) handleSearchKey(key rune) (line string, ok bool) {
	switch key {
	case keyReverseSearch:
		t.searchNext()
		return "", false
	case keyBackspace:
		t.searchBackspace()
		return "", false
	case keyAbortSearch, keyUnknown:
		t.endSearch(true)
		return "", false
	}
	if isPrintable(key) {
		t.searchAppend(key)
		return "", false
	}
	t.endSearch(false)
	return t.handleKey(key)
}
