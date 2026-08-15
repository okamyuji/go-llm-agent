package tool

import (
	"path/filepath"
	"testing"
)

func TestReadRegistry_MarkKnownThenIsKnown(t *testing.T) {
	r := NewReadRegistry()
	r.markKnown("/tmp/a/b.txt")
	if !r.isKnown("/tmp/a/b.txt") {
		t.Fatal("want isKnown=true after markKnown")
	}
}

func TestReadRegistry_UnknownPathIsNotKnown(t *testing.T) {
	r := NewReadRegistry()
	if r.isKnown("/tmp/never/marked.txt") {
		t.Fatal("want isKnown=false for unmarked path")
	}
}

func TestReadRegistry_NilReceiver_IsKnownFalse(t *testing.T) {
	var r *ReadRegistry
	if r.isKnown("/tmp/a") {
		t.Fatal("nil registry should always report unknown")
	}
}

func TestReadRegistry_NilReceiver_MarkKnownNoPanic(t *testing.T) {
	var r *ReadRegistry
	r.markKnown("/tmp/a") // should not panic
}

func TestReadRegistry_PathNormalization(t *testing.T) {
	r := NewReadRegistry()
	abs, err := filepath.Abs("testdata_readregistry/target.txt")
	if err != nil {
		t.Fatal(err)
	}
	r.markKnown(abs)
	// 末尾スラッシュや ./ を含む表記でも filepath.Clean 後は同一キーになる
	variant := filepath.Join(filepath.Dir(abs), ".", filepath.Base(abs))
	if !r.isKnown(variant) {
		t.Fatalf("want isKnown=true for normalized variant %q of %q", variant, abs)
	}
}

func TestReadRegistry_ConcurrentAccess_NoRace(t *testing.T) {
	r := NewReadRegistry()
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(n int) {
			p := filepath.Join("/tmp", "race", string(rune('a'+n%26)))
			r.markKnown(p)
			r.isKnown(p)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}
