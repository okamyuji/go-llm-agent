package secret

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

type envResolver struct {
	once    sync.Once
	dotenv  map[string]string
	envFile string
	loadErr error
}

// NewResolver env を一次、.env を二次として解決する Resolver を返す
func NewResolver(envFile string) Resolver {
	return &envResolver{envFile: envFile}
}

func (r *envResolver) load() {
	r.once.Do(func() {
		r.dotenv = map[string]string{}
		if r.envFile == "" {
			return
		}
		f, err := os.Open(r.envFile)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			r.loadErr = err
			return
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			v = strings.Trim(v, `"'`)
			r.dotenv[k] = v
		}
		if err := sc.Err(); err != nil {
			r.loadErr = err
		}
	})
}

// Resolve env 名から値を返す。env → .env → エラーの順で探す
func (r *envResolver) Resolve(envName string) (string, error) {
	if v, ok := os.LookupEnv(envName); ok && v != "" {
		return v, nil
	}
	r.load()
	if r.loadErr != nil {
		return "", r.loadErr
	}
	if v, ok := r.dotenv[envName]; ok && v != "" {
		return v, nil
	}
	return "", fmt.Errorf("secret %s が env にも .env にも存在しません", envName)
}
