package llm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
)

// MaxImageBytes 1 枚の画像の上限。履歴の画像は毎ターン base64 で送り直すため、メモリと送信量を抑える値にする
const MaxImageBytes = 20 << 20

// ErrImagesUnsupported 画像入力に対応しない provider が返すエラー
var ErrImagesUnsupported = errors.New("images not supported")

var supportedImageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// DataURL 画像を data URI にする
func (i Image) DataURL() string {
	return "data:" + i.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(i.Data)
}

// LoadImage path の画像を読み込む。形式は拡張子でなく中身で判定する
func LoadImage(path string) (Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return Image{}, err
	}
	if len(data) == 0 {
		return Image{}, fmt.Errorf("%s is empty", path)
	}
	if len(data) > MaxImageBytes {
		return Image{}, fmt.Errorf("%s exceeds %d bytes", path, MaxImageBytes)
	}
	mime := http.DetectContentType(data)
	if !slices.Contains(supportedImageTypes, mime) {
		return Image{}, fmt.Errorf("%s has unsupported type %s (png, jpeg, gif, webp only)", path, mime)
	}
	return Image{Name: path, MIMEType: mime, Data: data}, nil
}

// RejectImages msgs に画像があれば ErrImagesUnsupported を返す
func RejectImages(provider string, msgs []Message) error {
	for _, m := range msgs {
		if len(m.Images) > 0 {
			return fmt.Errorf("%w by %s", ErrImagesUnsupported, provider)
		}
	}
	return nil
}
