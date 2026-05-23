package agent

import "testing"

func TestRouter_RatioZeroNeverUsesCanary(t *testing.T) {
	t.Parallel()
	r := NewRouter("primary", "canary", 0, "", 0)
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
	r := NewRouter("primary", "canary", 1.0, "", 0)
	for i := range 50 {
		d := r.Pick(int64(i))
		if !d.UseCanary || d.Primary != "canary" {
			t.Fatalf("ratio=1 must always use canary, got %+v", d)
		}
	}
}

func TestRouter_ShadowAlwaysOrNever(t *testing.T) {
	t.Parallel()
	r0 := NewRouter("p", "", 0, "s", 0)
	if d := r0.Pick(1); d.Shadow != "" {
		t.Errorf("ratio=0 must yield empty Shadow, got %q", d.Shadow)
	}
	r1 := NewRouter("p", "", 0, "s", 1.0)
	if d := r1.Pick(1); d.Shadow != "s" {
		t.Errorf("ratio=1 must always set Shadow, got %q", d.Shadow)
	}
}

func TestRouter_ShadowRatioCappedAt05(t *testing.T) {
	t.Parallel()
	r := NewRouter("p", "", 0, "s", 0.9)
	// 内部状態はテストで直接確認できないので Pick 結果のみ観測
	// 0.9 のままだったら 90% で shadow になる
	on := 0
	for i := range 100 {
		if r.Pick(int64(i)).Shadow != "" {
			on++
		}
	}
	// 0.5 cap で 100 件中 70 件未満になることを期待
	if on >= 75 {
		t.Errorf("expected shadow capped at 0.5, observed %d/100", on)
	}
}

func TestRouter_DecisionDeterministicForSameSeed(t *testing.T) {
	t.Parallel()
	r := NewRouter("p", "c", 0.5, "s", 0.5)
	d1 := r.Pick(42)
	d2 := r.Pick(42)
	if d1 != d2 {
		t.Errorf("same seed must yield same decision, got %+v vs %+v", d1, d2)
	}
}
