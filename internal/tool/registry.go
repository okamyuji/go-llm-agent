package tool

// DefaultReadonlyTools 安全な readonly ツールのデフォルトセット
// enabled_tools が nil または空の場合はこれらのみを有効化する
var DefaultReadonlyTools = []string{"fs_read", "search_files", "http_fetch"}

type registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry tools の中から enabled に含まれるものだけを Registry に登録する
// enabled が nil または空の場合は DefaultReadonlyTools のみを有効にする (deny-by-default)
func NewRegistry(tools []Tool, enabled []string) Registry {
	effective := enabled
	if len(effective) == 0 {
		effective = DefaultReadonlyTools
	}
	set := map[string]struct{}{}
	for _, e := range effective {
		set[e] = struct{}{}
	}
	r := &registry{tools: map[string]Tool{}}
	for _, t := range tools {
		name := t.Spec().Name
		if _, ok := set[name]; ok {
			r.tools[name] = t
			r.order = append(r.order, name)
		}
	}
	return r
}

// List 有効なツールの Spec 一覧
func (r *registry) List() []Spec {
	out := make([]Spec, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n].Spec())
	}
	return out
}

// Lookup ツール名で検索する
func (r *registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}
