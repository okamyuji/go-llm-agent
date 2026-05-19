package secret

// Resolver シークレット解決インターフェース
type Resolver interface {
	Resolve(envName string) (string, error)
}
