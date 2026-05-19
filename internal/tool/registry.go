package tool

type registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry tools の中から enabled に含まれるものだけを Registry に登録する
func NewRegistry(tools []Tool, enabled []string) Registry {
	set := map[string]struct{}{}
	allowAll := enabled == nil
	for _, e := range enabled {
		set[e] = struct{}{}
	}
	r := &registry{tools: map[string]Tool{}}
	for _, t := range tools {
		name := t.Spec().Name
		if allowAll {
			r.tools[name] = t
			r.order = append(r.order, name)
			continue
		}
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
