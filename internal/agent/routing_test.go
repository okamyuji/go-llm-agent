package agent

import (
	"errors"
	"testing"
)

func mustRouter(t *testing.T, primary, canaryModel string, canaryRatio float64, shadowModel string, shadowRatio float64) *Router {
	t.Helper()
	r, err := NewRouter(primary, canaryModel, canaryRatio, shadowModel, shadowRatio)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

func TestRouter_RatioZeroNeverUsesCanary(t *testing.T) {
	t.Parallel()
	r := mustRouter(t, "primary", "canary", 0, "", 0)
	for i := range 100 {
		d := r.Pick(int64(i))
		if d.UseCanary {
			t.Fatalf("ratio=0 must never set UseCanary, seed=%d got %+v", i, d)
		}
		if d.Primary != "primary" {
			t.Fatalf("expected primary, got %q", d.Primary)
		}
	}
}

func TestRouter_RatioOneAlwaysUsesCanary(t *testing.T) {
	t.Parallel()
	r := mustRouter(t, "primary", "canary", 1.0, "", 0)
	for i := range 50 {
		d := r.Pick(int64(i))
		if !d.UseCanary || d.Primary != "canary" {
			t.Fatalf("ratio=1 must always use canary, got %+v", d)
		}
	}
}

func TestRouter_ShadowAlwaysOrNever(t *testing.T) {
	t.Parallel()
	r0 := mustRouter(t, "p", "", 0, "s", 0)
	if d := r0.Pick(1); d.Shadow != "" {
		t.Errorf("ratio=0 must yield empty Shadow, got %q", d.Shadow)
	}
	r1 := mustRouter(t, "p", "", 0, "s", 1.0)
	if d := r1.Pick(1); d.Shadow != "s" {
		t.Errorf("ratio=1 must always set Shadow, got %q", d.Shadow)
	}
}

func TestRouter_ShadowRatioCappedAt05(t *testing.T) {
	t.Parallel()
	r := mustRouter(t, "p", "", 0, "s", 0.9)
	// 内部状態はテストで直接確認できないので Pick 結果のみ観測
	// 0.9 のままだったら 90% で shadow になる
	on := 0
	for i := range 100 {
		if r.Pick(int64(i)).Shadow != "" {
			on++
		}
	}
	// 0.5 cap が効いていれば 100 件中 75 件未満に収まるはず
	// 厳密な 50/100 ではなく統計的な揺れを許容するため境界は 75 に置く
	if on >= 75 {
		t.Errorf("expected shadow capped at 0.5, observed %d/100", on)
	}
}

func TestRouter_DecisionDeterministicForSameSeed(t *testing.T) {
	t.Parallel()
	r := mustRouter(t, "p", "c", 0.5, "s", 0.5)
	d1 := r.Pick(42)
	d2 := r.Pick(42)
	if d1 != d2 {
		t.Errorf("same seed must yield same decision, got %+v vs %+v", d1, d2)
	}
}

// TestNewRouter_RejectsEmptyPrimary primary 空での構築を起動時 error で弾くことを確認する
func TestNewRouter_RejectsEmptyPrimary(t *testing.T) {
	t.Parallel()
	_, err := NewRouter("", "", 0, "", 0)
	if !errors.Is(err, ErrRouterPrimaryRequired) {
		t.Fatalf("expected ErrRouterPrimaryRequired, got %v", err)
	}
}
