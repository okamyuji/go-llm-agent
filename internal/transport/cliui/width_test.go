package cliui

import "testing"

func TestRuneWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1},
		{'Z', 1},
		{'1', 1},
		{' ', 1},
		{'あ', 2},  // ひらがな (East Asian Wide)
		{'漢', 2},  // 漢字
		{'Ａ', 2},  // 全角英字 (Fullwidth)
		{'、', 2},  // 全角句読点
		{'\n', 0}, // 制御文字
		{'\x1b', 0},
		{0x7f, 0}, // DEL
	}
	for _, c := range cases {
		if got := runeWidth(c.r); got != c.want {
			t.Errorf("runeWidth(%q)=%d, want %d", c.r, got, c.want)
		}
	}
}

func TestStringWidth(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"あい", 4},
		{"a あ b", 6},     // a(1)+space(1)+あ(2)+space(1)+b(1)
		{">> こんにちは", 13}, // >(1)>(1)space(1)+こんにちは(2*5=10)
	}
	for _, c := range cases {
		if got := stringWidth(c.s); got != c.want {
			t.Errorf("stringWidth(%q)=%d, want %d", c.s, got, c.want)
		}
	}
}
