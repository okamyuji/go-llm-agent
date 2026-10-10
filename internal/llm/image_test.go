package llm_test

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadImage_AcceptsSupportedFormats(t *testing.T) {
	cases := map[string]struct {
		data []byte
		mime string
	}{
		"png":  {pngHeader, "image/png"},
		"jpeg": {[]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), "image/jpeg"},
		"gif":  {[]byte("GIF89a\x01\x00\x01\x00"), "image/gif"},
		"webp": {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeFile(t, "img", c.data)
			img, err := llm.LoadImage(p)
			if err != nil {
				t.Fatalf("LoadImage: %v", err)
			}
			if img.MIMEType != c.mime || img.Name != p || string(img.Data) != string(c.data) {
				t.Errorf("got %+v", img)
			}
		})
	}
}

func TestLoadImage_Rejects(t *testing.T) {
	big := append(append([]byte{}, pngHeader...), make([]byte, llm.MaxImageBytes)...)
	cases := map[string]string{
		"missing":        filepath.Join(t.TempDir(), "nope.png"),
		"empty":          writeFile(t, "e.png", nil),
		"text as png":    writeFile(t, "t.png", []byte("hello world")),
		"over the limit": writeFile(t, "big.png", big),
		"directory":      t.TempDir(),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := llm.LoadImage(p); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestLoadImage_AcceptsExactlyMaxBytes(t *testing.T) {
	data := append(append([]byte{}, pngHeader...), make([]byte, llm.MaxImageBytes-len(pngHeader))...)
	if _, err := llm.LoadImage(writeFile(t, "max.png", data)); err != nil {
		t.Fatalf("LoadImage at limit: %v", err)
	}
}

func TestImage_DataURL(t *testing.T) {
	got := llm.Image{MIMEType: "image/png", Data: []byte("ab")}.DataURL()
	if got != "data:image/png;base64,YWI=" {
		t.Errorf("DataURL = %q", got)
	}
}

func TestRejectImages(t *testing.T) {
	plain := []llm.Message{{Role: llm.RoleUser, Content: "hi"}}
	if err := llm.RejectImages("gemini", plain); err != nil {
		t.Errorf("no images: %v", err)
	}
	withImg := []llm.Message{plain[0], {Role: llm.RoleUser, Images: []llm.Image{{MIMEType: "image/png"}}}}
	err := llm.RejectImages("gemini", withImg)
	if !errors.Is(err, llm.ErrImagesUnsupported) || !strings.Contains(err.Error(), "gemini") {
		t.Errorf("err = %v", err)
	}
}

// TestLoadImage_RejectsNonRegularFileWithoutBlocking FIFO は書き手が現れるまで Open が止まるため、開く前に拒否する
func TestLoadImage_RejectsNonRegularFileWithoutBlocking(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := llm.LoadImage(p)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error for a FIFO")
		}
	case <-time.After(2 * time.Second):
		// 止まった Open を解くため、書き手として開いて閉じる
		if f, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		<-done
		t.Fatal("LoadImage blocked on a FIFO")
	}
}

// TestImage_DataURLAllocatesOnce 20MB の画像を毎ターン data URL にするため、割り当てを 1 回に抑える
func TestImage_DataURLAllocatesOnce(t *testing.T) {
	img := llm.Image{MIMEType: "image/png", Data: make([]byte, 1<<16)}
	if n := testing.AllocsPerRun(10, func() { _ = img.DataURL() }); n > 1 {
		t.Errorf("DataURL allocates %.0f times, want 1", n)
	}
}

// TestImage_DataURLGrowsExactly 大きな割り当ては 8KB 単位に切り上がり、確保量の小さな不足が余りに隠れる。
// 出力を 128KB より 2 バイト長くし、確保量が足りなければ 128KB までしか切り上がらず 2 回目の割り当てが起きるようにする
func TestImage_DataURLGrowsExactly(t *testing.T) {
	const want = 128<<10 + 2
	img := llm.Image{MIMEType: "image/png", Data: make([]byte, 98289)}
	if got := len(img.DataURL()); got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	if n := testing.AllocsPerRun(10, func() { _ = img.DataURL() }); n > 1 {
		t.Errorf("DataURL allocates %.0f times, want 1", n)
	}
}

func TestImage_DataURLMatchesStdEncodingAcrossChunks(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 3071, 3072, 3073, 6145, 100000} {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(i * 7)
		}
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		if got := (llm.Image{MIMEType: "image/png", Data: data}).DataURL(); got != want {
			t.Errorf("n=%d: DataURL differs from base64.StdEncoding", n)
		}
	}
}
