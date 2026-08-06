package main

import (
	"bytes"
	"compress/zlib"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewImageAndSet(t *testing.T) {
	im := NewImage(10, 10)
	if len(im.Pix) != 300 {
		t.Fatalf("Pix 长度 = %d, want 300", len(im.Pix))
	}
	im.Set(0, 0, RGB{R: 1, G: 2, B: 3})
	c := im.Get(0, 0)
	if c.R != 1 || c.G != 2 || c.B != 3 {
		t.Fatalf("Set/Get 不一致: %+v", c)
	}
	// 越界不 panic
	im.Set(-1, -1, RGB{R: 9, G: 9, B: 9})
	im.Set(100, 100, RGB{R: 9, G: 9, B: 9})
}

func TestFillAndRect(t *testing.T) {
	im := NewImage(20, 20)
	im.Fill(RGB{R: 10, G: 20, B: 30})
	if im.Get(5, 5).R != 10 {
		t.Fatal("Fill 未生效")
	}
	im.Rect(0, 0, 3, 3, RGB{R: 255, G: 0, B: 0})
	if im.Get(0, 0).R != 255 || im.Get(3, 3).G != 0 {
		t.Fatal("Rect 角点颜色错误")
	}
	if im.Get(10, 10).R != 10 {
		t.Fatal("Rect 不应覆盖外部区域")
	}
}

func TestParseColor(t *testing.T) {
	c, err := parseColor("#ff8000")
	if err != nil {
		t.Fatalf("parseColor 报错: %v", err)
	}
	if c.R != 255 || c.G != 128 || c.B != 0 {
		t.Fatalf("parseColor 解析错误: %+v", c)
	}
	if _, err := parseColor("xyz"); err == nil {
		t.Fatal("非法颜色应报错")
	}
}

func TestSaveProducesValidPNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.png")
	im := NewImage(16, 16)
	im.Fill(RGB{})
	im.Rect(2, 2, 8, 8, RGB{R: 255, G: 255, B: 255})
	if err := im.Save(path); err != nil {
		t.Fatalf("Save 报错: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	// PNG 签名
	sig := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	if !bytes.HasPrefix(data, sig) {
		t.Fatal("不是合法 PNG 签名")
	}
	if !bytes.Contains(data, []byte("IHDR")) || !bytes.Contains(data, []byte("IDAT")) || !bytes.Contains(data, []byte("IEND")) {
		t.Fatal("缺少必要 PNG 数据块")
	}
	if len(data) < 50 {
		t.Fatalf("PNG 文件过小: %d 字节", len(data))
	}
}

func TestDrawText(t *testing.T) {
	im := NewImage(60, 20)
	im.Fill(RGB{})
	im.drawText(2, 2, "A1", RGB{R: 255, G: 255, B: 255})
	lit := false
	for y := 2; y < 9; y++ {
		for x := 2; x < 20; x++ {
			if im.Get(x, y).R == 255 {
				lit = true
				break
			}
		}
	}
	if !lit {
		t.Fatal("drawText 未点亮任何像素")
	}
}

func TestZlibRoundTrip(t *testing.T) {
	data := []byte(strings.Repeat("hello go-png zlib test ", 200))
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatalf("zlib 写入失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib 关闭失败: %v", err)
	}
	compressed := buf.Bytes()
	r, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("zlib.NewReader 失败: %v", err)
	}
	defer r.Close()
	imported, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("inflate 失败: %v", err)
	}
	if !bytes.Equal(imported, data) {
		t.Fatalf("zlib 往返不一致: len=%d vs %d", len(imported), len(data))
	}
	if len(compressed) >= len(data) {
		t.Fatalf("压缩未生效: 压缩后 %d >= 原始 %d", len(compressed), len(data))
	}
}
