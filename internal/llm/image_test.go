package llm_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
