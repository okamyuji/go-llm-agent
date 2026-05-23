package agent

// Decision Router.Pick が返すルーティング判定
type Decision struct {
	Primary   string
	Shadow    string
	UseCanary bool
}

// Router canary / shadow デプロイのルーティングロジック
// Pick は seed から決定論的に判定し、テスト容易性を担保する
type Router struct {
	primary     string
	canaryModel string
	canaryRatio float64
	shadowModel string
	shadowRatio float64
}

// NewRouter primary model と canary/shadow 設定から Router を構築する
// shadowRatio は安全のため 0.5 を上限としてハードキャップする
func NewRouter(primary, canaryModel string, canaryRatio float64, shadowModel string, shadowRatio float64) *Router {
	if shadowRatio > 0.5 {
		shadowRatio = 0.5
	}
	return &Router{
		primary:     primary,
		canaryModel: canaryModel,
		canaryRatio: canaryRatio,
		shadowModel: shadowModel,
		shadowRatio: shadowRatio,
	}
}

// Pick seed から決定論的に Decision を返す
// canaryRatio<=0 では canary は発火せず、>=1.0 では必ず発火する
// shadowRatio についても同様に決定する
func (r *Router) Pick(seed int64) Decision {
	d := Decision{Primary: r.primary}
	if r.canaryRatio >= 1.0 {
		d.UseCanary = true
		d.Primary = r.canaryModel
	} else if r.canaryRatio > 0 {
		bucket := normalize(seed, 0)
		if bucket < r.canaryRatio {
			d.UseCanary = true
			d.Primary = r.canaryModel
		}
	}
	if r.shadowRatio >= 1.0 {
		d.Shadow = r.shadowModel
	} else if r.shadowRatio > 0 {
		bucket := normalize(seed, 1)
		if bucket < r.shadowRatio {
			d.Shadow = r.shadowModel
		}
	}
	return d
}

// normalize seed と offset から 0.0-1.0 の決定論的乱数を返す
// splitmix64 風の avalanche を適用して低い seed でも上位 bit を撹拌する
// int64 -> uint64 のラップアラウンドは意図的なビット再解釈
func normalize(seed int64, offset int64) float64 {
	v := uint64(seed)*2654435761 + uint64(offset)*40499 + 0x9e3779b97f4a7c15 //nolint:gosec // intentional bit-reinterpretation
	v ^= v >> 33
	v *= 0xff51afd7ed558ccd
	v ^= v >> 33
	v *= 0xc4ceb9fe1a85ec53
	v ^= v >> 33
	return float64(v>>11) / float64(1<<53)
}
