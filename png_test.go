package main

import (
	"bytes"
	"compress/zlib"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustImage(t *testing.T, w, h int) *Image {
	t.Helper()
	im, err := NewImage(w, h)
	if err != nil {
		t.Fatalf("NewImage(%d,%d): %v", w, h, err)
	}
	return im
}

func TestNewImageAndSet(t *testing.T) {
	im := mustImage(t, 10, 10)
	if len(im.Pix) != 300 {
		t.Fatalf("Pix 长度 = %d, want 300", len(im.Pix))
	}
	im.Set(0, 0, RGB{R: 1, G: 2, B: 3})
	if c := im.Get(0, 0); c.R != 1 || c.G != 2 || c.B != 3 {
		t.Fatalf("Set/Get 不一致: %+v", c)
	}
	// 越界不 panic
	im.Set(-1, -1, RGB{R: 9, G: 9, B: 9})
	im.Set(100, 100, RGB{R: 9, G: 9, B: 9})
	if c := im.Get(-1, -1); c != (RGB{}) {
		t.Fatalf("越界 Get 应返回零值, got %+v", c)
	}
}

// NewImage 现在会拒绝非法尺寸。
// 之前 0 宽/0 高会生成一个标准库都拒绝解码的 PNG，
// 巨大尺寸则会尝试分配几十 GB。
func TestNewImageRejectsBadSize(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"零宽", 0, 5},
		{"零高", 5, 0},
		{"负宽", -1, 5},
		{"负高", 5, -1},
		{"两个都是零", 0, 0},
		{"超出像素上限", 100000, 100000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewImage(c.w, c.h); err == nil {
				t.Fatalf("NewImage(%d,%d) 应该报错", c.w, c.h)
			}
		})
	}
	// 边界：刚好等于上限应该允许
	if _, err := NewImage(maxPixels, 1); err != nil {
		t.Fatalf("恰好达到上限不该报错: %v", err)
	}
	if _, err := NewImage(maxPixels+1, 1); err == nil {
		t.Fatal("超过上限 1 像素应该报错")
	}
}

func TestFillAndRect(t *testing.T) {
	im := mustImage(t, 20, 20)
	im.Fill(RGB{R: 10, G: 20, B: 30})
	if im.Get(5, 5).R != 10 {
		t.Fatal("Fill 未生效")
	}
	im.Rect(0, 0, 3, 3, RGB{R: 255})
	if im.Get(0, 0).R != 255 || im.Get(3, 3).R != 255 {
		t.Fatal("Rect 角点应被填充（含边界）")
	}
	if im.Get(4, 4).R != 10 {
		t.Fatal("Rect 不应覆盖外部区域")
	}
}

// Rect 传反的坐标应该自动交换。
func TestRectSwapsCoords(t *testing.T) {
	a := mustImage(t, 10, 10)
	b := mustImage(t, 10, 10)
	a.Rect(2, 2, 6, 6, RGB{R: 255})
	b.Rect(6, 6, 2, 2, RGB{R: 255}) // 反着给
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("Rect 应对调换后的坐标产生相同结果")
	}
}

// Rect 越界不该 panic，且只画可见部分。
func TestRectClipsOutOfBounds(t *testing.T) {
	im := mustImage(t, 10, 10)
	im.Fill(RGB{})
	im.Rect(-100, -100, 200, 200, RGB{R: 255})
	for i := 0; i < len(im.Pix); i += 3 {
		if im.Pix[i] != 255 {
			t.Fatal("超大矩形应覆盖整张图")
		}
	}
}

func TestParseColor(t *testing.T) {
	cases := []struct {
		in      string
		want    RGB
		wantErr bool
	}{
		{"#ff8000", RGB{255, 128, 0}, false},
		{"ff8000", RGB{255, 128, 0}, false},
		{"#FF8000", RGB{255, 128, 0}, false},   // 大写
		{"  #ff8000  ", RGB{255, 128, 0}, false}, // 带空格
		{"#f0a", RGB{255, 0, 170}, false},      // 三位简写
		{"f0a", RGB{255, 0, 170}, false},
		{"#000", RGB{0, 0, 0}, false},
		{"#fff", RGB{255, 255, 255}, false},
		{"xyz", RGB{}, true},
		{"#12345", RGB{}, true},
		{"#1234567", RGB{}, true},
		{"", RGB{}, true},
		{"+f0000", RGB{}, true}, // ParseUint 会接受符号，必须自己挡掉
		{"-f0000", RGB{}, true},
		{"0xff00", RGB{}, true},
	}
	for _, c := range cases {
		got, err := parseColor(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseColor(%q) 应报错，却得到 %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseColor(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseColor(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// 这是本轮最关键的回归测试。
// 旧实现 uint8(float64(a) + t*float64(b-a)) 里 b-a 是 uint8 减法，
// b < a 时回绕：白(255)->黑(0) 的 b-a 变成 1，
// 于是整条渐变几乎全是白色，「白到黑」这个最常见的用法完全失效。
func TestLerpNoUnderflowOnDarkening(t *testing.T) {
	mid := lerpRGB(RGB{R: 255, G: 255, B: 255}, RGB{}, 0.5)
	for _, v := range []uint8{mid.R, mid.G, mid.B} {
		if v < 120 || v > 136 {
			t.Fatalf("白->黑 中点应约为 128，得到 %d（uint8 回绕 bug）", v)
		}
	}
	// 两端必须精确
	if got := lerpRGB(RGB{R: 255}, RGB{}, 0); got.R != 255 {
		t.Fatalf("t=0 应返回起始色, got R=%d", got.R)
	}
	if got := lerpRGB(RGB{R: 255}, RGB{}, 1); got.R != 0 {
		t.Fatalf("t=1 应返回结束色, got R=%d", got.R)
	}
}

func TestLerpClampsT(t *testing.T) {
	a := RGB{R: 10, G: 10, B: 10}
	b := RGB{R: 200, G: 200, B: 200}
	if got := lerpRGB(a, b, -5); got != a {
		t.Fatalf("t<0 应钳到起始色, got %+v", got)
	}
	if got := lerpRGB(a, b, 5); got != b {
		t.Fatalf("t>1 应钳到结束色, got %+v", got)
	}
}

// 渐变的两个角必须真的是给定的两端色。
// 旧代码分母用 W+H，而 x+y 最大只有 W+H-2，
// 所以右下角永远到不了结束色。
func TestGradientReachesBothEnds(t *testing.T) {
	a := RGB{R: 255, G: 0, B: 0}
	b := RGB{R: 0, G: 0, B: 255}
	im := mustImage(t, 32, 32)
	im.gradient(a, b)

	if got := im.Get(0, 0); got != a {
		t.Fatalf("左上角应为起始色 %+v, got %+v", a, got)
	}
	if got := im.Get(31, 31); got != b {
		t.Fatalf("右下角应为结束色 %+v, got %+v", b, got)
	}
}

// 白->黑渐变在整图层面上必须真的有明暗变化，不能是一整片纯白。
func TestGradientWhiteToBlackActuallyVaries(t *testing.T) {
	im := mustImage(t, 64, 64)
	im.gradient(RGB{R: 255, G: 255, B: 255}, RGB{})

	var min, max uint8 = 255, 0
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			v := im.Get(x, y).R
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
	}
	if max-min < 200 {
		t.Fatalf("白->黑渐变明暗跨度只有 %d（min=%d max=%d），说明发生了 uint8 回绕", max-min, min, max)
	}
}

// 单像素图渐变不能除零得到 NaN。
func TestGradientSinglePixel(t *testing.T) {
	im := mustImage(t, 1, 1)
	im.gradient(RGB{R: 200}, RGB{B: 100})
	c := im.Get(0, 0)
	if c.R != 200 || c.B != 0 {
		t.Fatalf("1x1 渐变应取起始色, got %+v", c)
	}
}

// 生成的 PNG 必须能被标准库 image/png 解码，
// 这一次性验证了签名、IHDR、CRC、zlib 流、IEND 全部正确。
// 原来的测试只 bytes.Contains 找 "IHDR" 字样，CRC 算错也发现不了。
func TestSaveDecodableByStdlib(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.png")

	im := mustImage(t, 16, 12)
	im.Fill(RGB{R: 10, G: 20, B: 30})
	im.Rect(2, 2, 8, 8, RGB{R: 255, G: 128, B: 64})
	if err := im.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("标准库解码失败: %v", err)
	}
	bnds := img.Bounds()
	if bnds.Dx() != 16 || bnds.Dy() != 12 {
		t.Fatalf("尺寸不符: %dx%d", bnds.Dx(), bnds.Dy())
	}
	// 抽查两个像素，确认颜色和填充一致
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 != 10 || g>>8 != 20 || b>>8 != 30 {
		t.Fatalf("背景像素错误: %d,%d,%d", r>>8, g>>8, b>>8)
	}
	r, g, b, _ = img.At(5, 5).RGBA()
	if r>>8 != 255 || g>>8 != 128 || b>>8 != 64 {
		t.Fatalf("矩形像素错误: %d,%d,%d", r>>8, g>>8, b>>8)
	}
}

// 逐像素比对：编码 -> 标准库解码，必须完全一致。
func TestEncodeRoundTripPixelPerfect(t *testing.T) {
	im := mustImage(t, 23, 17) // 故意用非 2 的幂，暴露行跨度错误
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			im.Set(x, y, RGB{R: uint8(x * 7), G: uint8(y * 11), B: uint8(x ^ y)})
		}
	}

	var buf bytes.Buffer
	if err := im.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("解码: %v", err)
	}
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			want := im.Get(x, y)
			r, g, b, _ := img.At(x, y).RGBA()
			if uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b>>8) != want.B {
				t.Fatalf("(%d,%d) 不一致: got %d,%d,%d want %+v", x, y, r>>8, g>>8, b>>8, want)
			}
		}
	}
}

// 写入失败必须向上报错。
// 原来 writeChunk 没有返回值，所有写错误被吞掉，Save 永远返回 nil。
type errWriter struct {
	failAfter int
	n         int
}

func (w *errWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.n > w.failAfter {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

func TestEncodeReportsWriteError(t *testing.T) {
	im := mustImage(t, 64, 64)
	im.Fill(RGB{R: 1, G: 2, B: 3})
	// 缓冲区默认 4096，设小一点确保 Flush 时真的会往下写
	if err := im.Encode(&errWriter{failAfter: 10}); err == nil {
		t.Fatal("底层写失败时 Encode 必须返回错误")
	}
}

// Save 失败时不该留下半截损坏的文件。
func TestSaveToBadPathReturnsError(t *testing.T) {
	im := mustImage(t, 4, 4)
	// 往一个不存在的目录里写
	bad := filepath.Join(t.TempDir(), "nope", "x.png")
	if err := im.Save(bad); err == nil {
		t.Fatal("写入不存在的目录应报错")
	}
}

func TestDrawText(t *testing.T) {
	im := mustImage(t, 60, 20)
	im.Fill(RGB{})
	w := im.drawText(2, 2, "A1", RGB{R: 255, G: 255, B: 255})
	if w != TextWidth("A1") {
		t.Fatalf("drawText 返回宽度 %d, want %d", w, TextWidth("A1"))
	}
	lit := 0
	for y := 2; y < 2+glyphH; y++ {
		for x := 2; x < 2+w; x++ {
			if im.Get(x, y).R == 255 {
				lit++
			}
		}
	}
	if lit == 0 {
		t.Fatal("drawText 未点亮任何像素")
	}
}

// 小写字母原来全部被画成 '?'，因为字体表只有大写键。
func TestDrawTextHandlesLowercase(t *testing.T) {
	lower := mustImage(t, 60, 20)
	upper := mustImage(t, 60, 20)
	lower.Fill(RGB{})
	upper.Fill(RGB{})
	lower.drawText(1, 1, "abc", RGB{R: 255, G: 255, B: 255})
	upper.drawText(1, 1, "ABC", RGB{R: 255, G: 255, B: 255})
	if !bytes.Equal(lower.Pix, upper.Pix) {
		t.Fatal("小写字母应回退到同形大写字形，而不是画成 ?")
	}

	// 并且不能和 "???" 一样
	q := mustImage(t, 60, 20)
	q.Fill(RGB{})
	q.drawText(1, 1, "???", RGB{R: 255, G: 255, B: 255})
	if bytes.Equal(lower.Pix, q.Pix) {
		t.Fatal("小写字母被画成了 ???")
	}
}

// 常用符号应该有真实字形，而不是回退到 '?'。
func TestFontCoversCommonSymbols(t *testing.T) {
	q := font5x7['?']
	for _, r := range []rune{',', ';', '_', '+', '=', '*', '/', '(', ')', '[', ']', '<', '>', '#', '%', '@', '&', '$'} {
		g, ok := font5x7[r]
		if !ok {
			t.Errorf("字体缺少 %q", r)
			continue
		}
		if g == q {
			t.Errorf("%q 的字形和 '?' 相同，等于没有实现", r)
		}
	}
}

// 画到画布外不能 panic。
func TestDrawTextOutOfBounds(t *testing.T) {
	im := mustImage(t, 10, 10)
	im.drawText(-50, -50, "HELLO", RGB{R: 255})
	im.drawText(1000, 1000, "HELLO", RGB{R: 255})
	im.drawText(8, 8, "LONGTEXT", RGB{R: 255}) // 右边超出
}

func TestTextWidth(t *testing.T) {
	if got := TextWidth(""); got != 0 {
		t.Fatalf("空串宽度应为 0, got %d", got)
	}
	if got := TextWidth("A"); got != glyphW {
		t.Fatalf("单字符宽度应为 %d, got %d", glyphW, got)
	}
	if got := TextWidth("AB"); got != glyphW*2+glyphGap {
		t.Fatalf("两字符宽度错误: %d", got)
	}
}

func TestParseRect(t *testing.T) {
	nums, c, err := parseRect("1,2,3,4,#ff0000")
	if err != nil {
		t.Fatalf("parseRect 报错: %v", err)
	}
	if nums != [4]int{1, 2, 3, 4} {
		t.Fatalf("坐标错误: %v", nums)
	}
	if c != (RGB{R: 255}) {
		t.Fatalf("颜色错误: %+v", c)
	}

	for _, bad := range []string{"1,2,3,4", "1,2,3,4,5,6", "a,2,3,4,#fff", "1,2,3,4,zzz", ""} {
		if _, _, err := parseRect(bad); err == nil {
			t.Errorf("parseRect(%q) 应报错", bad)
		}
	}
}

// 三位简写颜色在 -rect 里也要能用。
func TestParseRectShorthandColor(t *testing.T) {
	_, c, err := parseRect("0,0,1,1,#0f0")
	if err != nil {
		t.Fatalf("parseRect: %v", err)
	}
	if c != (RGB{G: 255}) {
		t.Fatalf("三位简写解析错误: %+v", c)
	}
}

func TestParseAt(t *testing.T) {
	x, y, err := parseAt("12,34")
	if err != nil || x != 12 || y != 34 {
		t.Fatalf("parseAt(\"12,34\") = %d,%d,%v", x, y, err)
	}
	if x, y, err := parseAt(" 5 , 6 "); err != nil || x != 5 || y != 6 {
		t.Fatalf("parseAt 应容忍空格: %d,%d,%v", x, y, err)
	}
	// 原来这些都会被静默忽略并回退到 10,10
	for _, bad := range []string{"abc,def", "10", "", "10,", ",10", "1,2,3x"} {
		if _, _, err := parseAt(bad); err == nil {
			t.Errorf("parseAt(%q) 应报错", bad)
		}
	}
}

func TestParseGrad(t *testing.T) {
	a, b, err := parseGrad("#ff0000,#0000ff")
	if err != nil {
		t.Fatalf("parseGrad: %v", err)
	}
	if a != (RGB{R: 255}) || b != (RGB{B: 255}) {
		t.Fatalf("解析错误: %+v %+v", a, b)
	}
	for _, bad := range []string{"#ff0000", "", "zzz,#fff", "#fff,zzz"} {
		if _, _, err := parseGrad(bad); err == nil {
			t.Errorf("parseGrad(%q) 应报错", bad)
		}
	}
}

// 端到端：跑一遍 run()，确认产物能被标准库解码。
func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "e2e.png")
	err := run(out, 120, 60, "#222", "", "5,5,40,30,#0f0", "Hello 1", "#fff", "50,20")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := png.Decode(f); err != nil {
		t.Fatalf("产物无法解码: %v", err)
	}
}

func TestRunRejectsBadArgs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "x.png")
	cases := []struct {
		name string
		fn   func() error
	}{
		{"缺 out", func() error { return run("", 10, 10, "#fff", "", "", "", "#fff", "0,0") }},
		{"零宽", func() error { return run(out, 0, 10, "#fff", "", "", "", "#fff", "0,0") }},
		{"坏 bg", func() error { return run(out, 10, 10, "zzz", "", "", "", "#fff", "0,0") }},
		{"坏 grad", func() error { return run(out, 10, 10, "#fff", "zzz", "", "", "#fff", "0,0") }},
		{"坏 rect", func() error { return run(out, 10, 10, "#fff", "", "bad", "", "#fff", "0,0") }},
		{"坏 fg", func() error { return run(out, 10, 10, "#fff", "", "", "hi", "zzz", "0,0") }},
		{"坏 at", func() error { return run(out, 10, 10, "#fff", "", "", "hi", "#fff", "abc") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatal("应该报错")
			}
		})
	}
}

// -grad 时用渐变，不用 -bg。
func TestRunGradientPath(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "g.png")
	if err := run(out, 40, 40, "#222", "#ffffff,#000000", "", "", "#fff", "0,0"); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("解码: %v", err)
	}
	tl, _, _, _ := img.At(0, 0).RGBA()
	br, _, _, _ := img.At(39, 39).RGBA()
	if tl>>8 != 255 {
		t.Fatalf("左上角应为白, got %d", tl>>8)
	}
	if br>>8 != 0 {
		t.Fatalf("右下角应为黑, got %d", br>>8)
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
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("inflate 失败: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("zlib 往返不一致: len=%d vs %d", len(got), len(data))
	}
	if len(compressed) >= len(data) {
		t.Fatalf("压缩未生效: %d >= %d", len(compressed), len(data))
	}
}

// 压缩确实生效：纯色大图应该被压得很小。
func TestIDATIsCompressed(t *testing.T) {
	im := mustImage(t, 200, 200)
	im.Fill(RGB{R: 7, G: 7, B: 7})
	var buf bytes.Buffer
	if err := im.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	rawSize := (200*3 + 1) * 200
	if buf.Len() > rawSize/10 {
		t.Fatalf("纯色图压缩效果太差: %d 字节 (原始 %d)", buf.Len(), rawSize)
	}
}
