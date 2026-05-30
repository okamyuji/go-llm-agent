package secret

import (
	"fmt"
	"strings"
)

// Resolver シークレット解決インターフェース
type Resolver interface {
	Resolve(envName string) (string, error)
}

// ResolveAny resolves the first available secret from envNames and returns the value plus the env name used.
func ResolveAny(r Resolver, envNames ...string) (string, string, error) {
	var tried []string
	for _, name := range envNames {
		if name == "" {
			continue
		}
		tried = append(tried, name)
		value, err := r.Resolve(name)
		if err == nil && value != "" {
			return value, name, nil
		}
	}
	return "", "", fmt.Errorf("secret not found in any configured env: %s", strings.Join(tried, ", "))
}
