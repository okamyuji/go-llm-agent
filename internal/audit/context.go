package audit

import (
	"context"
	"regexp"
)

type sessionIDKey struct{}

// sessionIDPattern WAL のディレクトリ名と Iggy の topic 名に使うため、パス区切りや制御文字を許さない
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// WithSessionID セッション ID を ctx に載せる
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, id)
}

// SessionIDFrom ctx からセッション ID を取り出す。無ければ空文字
func SessionIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey{}).(string)
	return v
}

// NormalizeSessionID 規則に合わない ID を run-<runID> に置き換える。第 2 戻り値は置き換えたか
func NormalizeSessionID(id, runID string) (string, bool) {
	if sessionIDPattern.MatchString(id) {
		return id, false
	}
	return "run-" + runID, true
}
