package secret

import "strings"

var sensitiveSuffixes = []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD"}

// MaskString シークレットらしい文字列を *** に置き換える
func MaskString(v string) string {
	if strings.HasPrefix(v, "sk-") {
		return "***"
	}
	return v
}

// MaskMap キー名がシークレット風のエントリの値を *** に置き換えた map を返す
func MaskMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
		upper := strings.ToUpper(k)
		for _, suf := range sensitiveSuffixes {
			if strings.HasSuffix(upper, suf) {
				out[k] = "***"
				break
			}
		}
	}
	return out
}
