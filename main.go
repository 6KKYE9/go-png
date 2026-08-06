// go-png 是 PNG 图片生成小工具（用标准库自己写 PNG 编码）。
// 支持：纯色背景、渐变背景、画实心矩形、画点阵文字（内置 5x7 像素字体）。
// 输出标准 PNG 文件（RGB，IDAT 用标准库 zlib 压缩，浏览器/系统均可识别）。
package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// RGB 是一个 24 位颜色。
type RGB struct {
	R, G, B uint8
}

// Image 是一张 RGB 位图。
type Image struct {
	W, H int
	Pix  []byte // 长度 W*H*3，按行优先 RGB
}

// maxPixels 限制单张图的像素数，防止 -w/-h 给个巨大值直接把内存吃干。
// 8000 万像素 ≈ 240MB 位图，够用且不至于把机器拖死。
const maxPixels = 80_000_000

// NewImage 创建一张给定尺寸的黑色图片。
// 尺寸非法时返回 error —— 原来直接 make([]byte, w*h*3)：
// 宽高为 0 会生成标准库都拒绝解码的非法 PNG，
// 宽高极大则会溢出或试图分配几十 GB。
func NewImage(w, h int) (*Image, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("宽高必须为正数，得到 %dx%d", w, h)
	}
	// 先用 int64 判断，避免 int 乘法在计算过程中就已经溢出。
	if int64(w)*int64(h) > maxPixels {
		return nil, fmt.Errorf("图片过大: %dx%d = %d 像素，上限 %d", w, h, int64(w)*int64(h), maxPixels)
	}
	return &Image{W: w, H: h, Pix: make([]byte, w*h*3)}, nil
}

// Set 设置某像素颜色（越界忽略）。
func (im *Image) Set(x, y int, c RGB) {
	if x < 0 || y < 0 || x >= im.W || y >= im.H {
		return
	}
	i := (y*im.W + x) * 3
	im.Pix[i] = c.R
	im.Pix[i+1] = c.G
	im.Pix[i+2] = c.B
}

// Get 取某像素颜色。
func (im *Image) Get(x, y int) RGB {
	if x < 0 || y < 0 || x >= im.W || y >= im.H {
		return RGB{}
	}
	i := (y*im.W + x) * 3
	return RGB{R: im.Pix[i], G: im.Pix[i+1], B: im.Pix[i+2]}
}

// Fill 用纯色填充整张图。
func (im *Image) Fill(c RGB) {
	for i := 0; i < len(im.Pix); i += 3 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2] = c.R, c.G, c.B
	}
}

// Rect 画一个实心矩形（含边框）。
func (im *Image) Rect(x0, y0, x1, y1 int, c RGB) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			im.Set(x, y, c)
		}
	}
}

// gradient 生成从左上到右下的线性渐变。
func (im *Image) gradient(a, b RGB) {
	// 分母原来是 W+H，但 x+y 的最大值是 (W-1)+(H-1)，
	// 所以 t 永远到不了 1，右下角始终差一截，渐变末端色出不来。
	// 单像素图（W+H-2 == 0）要特判，否则除零得到 NaN。
	den := float64(im.W + im.H - 2)
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			t := 0.0
			if den > 0 {
				t = float64(x+y) / den
			}
			im.Set(x, y, lerpRGB(a, b, t))
		}
	}
}

// lerpRGB 在两色之间做线性插值。
func lerpRGB(a, b RGB, t float64) RGB {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return RGB{
		R: lerpU8(a.R, b.R, t),
		G: lerpU8(a.G, b.G, t),
		B: lerpU8(a.B, b.B, t),
	}
}

// lerpU8 是单通道插值。
// 原写法 uint8(float64(a) + t*float64(b-a)) 有个致命问题：
// b-a 是 uint8 减法，b < a 时会回绕。比如白(255)->黑(0)，
// b-a = 0-255 = 1（uint8），插值结果全程约等于 255，
// 于是「白到黑的渐变」渲染出来是一整片纯白。
// 必须先各自转成 float64 再相减。
func lerpU8(a, b uint8, t float64) uint8 {
	v := float64(a) + t*(float64(b)-float64(a))
	// 浮点误差可能让 v 略微越界，钳一下再转换。
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5) // 四舍五入，避免整体偏暗
}

// pngSignature 是 PNG 文件头的 8 字节魔数。
var pngSignature = []byte{137, 80, 78, 71, 13, 10, 26, 10}

// Encode 把图片编码成 PNG 字节流写入 w。
// 拆出来是为了让 Save 之外的调用方（以及测试）能直接编码到内存，
// 不必先落盘再读回。
func (im *Image) Encode(w io.Writer) error {
	bw := bufio.NewWriter(w)

	if _, err := bw.Write(pngSignature); err != nil {
		return fmt.Errorf("写 PNG 签名: %w", err)
	}

	// IHDR
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(im.W))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(im.H))
	ihdr[8] = 8 // 位深
	ihdr[9] = 2 // 颜色类型 2 = Truecolor RGB
	// 10,11,12 = 压缩/滤波/交错，均为 0
	if err := writeChunk(bw, "IHDR", ihdr); err != nil {
		return err
	}

	// IDAT：每行前加过滤字节 0
	raw := make([]byte, 0, (im.W*3+1)*im.H)
	for y := 0; y < im.H; y++ {
		raw = append(raw, 0) // 滤波类型 None
		row := im.Pix[y*im.W*3 : (y+1)*im.W*3]
		raw = append(raw, row...)
	}
	// 用标准库 zlib 压缩 IDAT（替代原来的无压缩存储块，体积小很多）
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return fmt.Errorf("zlib 压缩: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("zlib 收尾: %w", err)
	}
	if err := writeChunk(bw, "IDAT", buf.Bytes()); err != nil {
		return err
	}

	if err := writeChunk(bw, "IEND", nil); err != nil {
		return err
	}
	return bw.Flush()
}

// Save 把图片编码成 PNG 并写入文件。
func (im *Image) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// 原来只有 defer f.Close()，Close 的错误被丢掉了。
	// 带缓冲的文件系统上，真正的写失败往往是在 Close 才报出来，
	// 吞掉它等于「明明没写成功却告诉用户成功了」。
	if err := im.Encode(f); err != nil {
		f.Close()
		os.Remove(path) // 别留下半截损坏的文件
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("关闭文件: %w", err)
	}
	return nil
}

// writeChunk 写入一个 PNG 数据块（长度+类型+数据+CRC）。
// 原来这个函数没有返回值，所有 w.Write 的错误全被忽略，
// 磁盘满或只读时 Save 依然返回 nil。现在逐个检查。
func writeChunk(w *bufio.Writer, typ string, data []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("写 %s 长度: %w", typ, err)
	}
	typeBytes := []byte(typ)
	if _, err := w.Write(typeBytes); err != nil {
		return fmt.Errorf("写 %s 类型: %w", typ, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("写 %s 数据: %w", typ, err)
	}
	// CRC 覆盖「类型 + 数据」。原来写成 crc32.ChecksumIEEE(append(typeBytes, data...))，
	// 结果正确，但会为整个 IDAT 再复制一份（大图上白白多占一倍内存）。
	// 用 crc32.Update 分两段累加，零拷贝。
	crc := crc32.Update(0, crc32.IEEETable, typeBytes)
	crc = crc32.Update(crc, crc32.IEEETable, data)
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], crc)
	if _, err := w.Write(crcBuf[:]); err != nil {
		return fmt.Errorf("写 %s CRC: %w", typ, err)
	}
	return nil
}

// parseColor 把 "#rrggbb"、"rrggbb" 或三位简写 "#rgb" 解析成 RGB。
// 原来只认 6 位，且 " ff0000"（命令行里很容易多带空格）会直接报错。
func parseColor(s string) (RGB, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	// 三位简写：#f0a 等价于 #ff00aa，CSS 里的通用约定。
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return RGB{}, fmt.Errorf("颜色需为 #rgb 或 #rrggbb，得到 %q", s)
	}
	// ParseUint 会接受 "+ff000" 这类带符号的写法，先自己过一遍字符集。
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return RGB{}, fmt.Errorf("颜色含非十六进制字符 %q: %s", s[i], s)
		}
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("非法颜色: %s", s)
	}
	return RGB{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}, nil
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

const (
	glyphW    = 5 // 字形宽
	glyphH    = 7 // 字形高
	glyphGap  = 1 // 字间距
	glyphStep = glyphW + glyphGap
)

// TextWidth 返回一段文字按内置字体绘制后占的像素宽度。
// 供调用方在画之前判断会不会超出画布。
func TextWidth(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return n*glyphStep - glyphGap // 末尾不算间距
}

// drawText 在 (x,y) 起始位置用内置 5x7 字体画文字。
// 返回实际绘制的宽度，便于连续排版。
func (im *Image) drawText(x, y int, s string, c RGB) int {
	col := 0
	for _, r := range s {
		// 字体表只有大写，原来小写字母全部落到 '?'，
		// -text "hi" 会画成 "??"。这里先归一成大写再查表。
		glyph, ok := font5x7[r]
		if !ok {
			if up := unicode.ToUpper(r); up != r {
				glyph, ok = font5x7[up]
			}
		}
		if !ok {
			glyph = font5x7['?']
		}
		for gy := 0; gy < glyphH; gy++ {
			bits := glyph[gy]
			for gx := 0; gx < glyphW; gx++ {
				if bits&(1<<(glyphW-1-gx)) != 0 {
					im.Set(x+col+gx, y+gy, c)
				}
			}
		}
		col += glyphStep
	}
	return TextWidth(s)
}

func usage() {
	fmt.Print(`go-png 零依赖 PNG 生成器

用法:
  go-png -out out.png -w 200 -h 100 -bg #202020
  go-png -out out.png -w 200 -h 100 -grad #ff0000,#0000ff
  go-png -out out.png -w 200 -h 100 -bg #222 -rect 10,10,80,60,#00ff00
  go-png -out out.png -w 200 -h 100 -bg #222 -text "Hi" -fg #ffffff -at 20,20

选项:
  -out  输出文件路径（必填）
  -w    宽度（默认 200）
  -h    高度（默认 100）
  -bg   背景色，支持 #rgb / #rrggbb（默认 #202020）
  -grad 渐变两端色（逗号分隔，替代 -bg）
  -rect x0,y0,x1,y1,color  画一个实心矩形
  -text 要绘制的文字（字母大小写均可，缺字回退为 ?）
  -fg   文字颜色（默认 #ffffff）
  -at   文字位置 x,y（默认 10,10）
`)
}

func main() {
	out := flag.String("out", "", "输出文件路径")
	w := flag.Int("w", 200, "宽度")
	h := flag.Int("h", 100, "高度")
	bg := flag.String("bg", "#202020", "背景色")
	grad := flag.String("grad", "", "渐变两端色")
	rect := flag.String("rect", "", "矩形 x0,y0,x1,y1,color")
	text := flag.String("text", "", "文字")
	fg := flag.String("fg", "#ffffff", "文字颜色")
	at := flag.String("at", "10,10", "文字位置")

	flag.Parse()

	// 把逻辑收进 run()，错误统一在这里落地。
	// 原来 main 里散落着 9 处 fmt.Println(err) + os.Exit(1)，
	// 而且错误全打到 stdout —— 管道里 `go-png ... > x.png` 会把错误信息混进数据流。
	if err := run(*out, *w, *h, *bg, *grad, *rect, *text, *fg, *at); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s (%dx%d)\n", *out, *w, *h)
}

func run(out string, w, h int, bg, grad, rect, text, fg, at string) error {
	if out == "" {
		usage()
		return fmt.Errorf("缺少 -out")
	}
	im, err := NewImage(w, h)
	if err != nil {
		return err
	}

	if grad != "" {
		a, b, err := parseGrad(grad)
		if err != nil {
			return err
		}
		im.gradient(a, b)
	} else {
		c, err := parseColor(bg)
		if err != nil {
			return fmt.Errorf("-bg: %w", err)
		}
		im.Fill(c)
	}

	if rect != "" {
		nums, col, err := parseRect(rect)
		if err != nil {
			return err
		}
		im.Rect(nums[0], nums[1], nums[2], nums[3], col)
	}

	if text != "" {
		fgc, err := parseColor(fg)
		if err != nil {
			return fmt.Errorf("-fg: %w", err)
		}
		px, py, err := parseAt(at)
		if err != nil {
			return err
		}
		// 文字整体画到画布外时提醒一句 —— 原来是静默生成一张看不出问题的图。
		if px >= im.W || py >= im.H || px+TextWidth(text) <= 0 || py+glyphH <= 0 {
			fmt.Fprintf(os.Stderr, "提示: 文字位置 %d,%d 在 %dx%d 画布之外，将不可见\n", px, py, im.W, im.H)
		}
		im.drawText(px, py, text, fgc)
	}

	return im.Save(out)
}

// parseGrad 解析 "-grad colorA,colorB"。
func parseGrad(s string) (RGB, RGB, error) {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return RGB{}, RGB{}, fmt.Errorf("-grad 需要两端色，如 #ff0000,#0000ff")
	}
	a, err := parseColor(parts[0])
	if err != nil {
		return RGB{}, RGB{}, fmt.Errorf("-grad 起始色: %w", err)
	}
	b, err := parseColor(parts[1])
	if err != nil {
		return RGB{}, RGB{}, fmt.Errorf("-grad 结束色: %w", err)
	}
	return a, b, nil
}

// parseAt 解析 "-at x,y"。
// 原来解析失败会静默回退到 10,10 —— 用户 -at abc 得到一张位置不对的图却没有任何提示。
func parseAt(s string) (int, int, error) {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("-at 格式应为 x,y，得到 %q", s)
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("-at 的 x 需为整数: %q", parts[0])
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("-at 的 y 需为整数: %q", parts[1])
	}
	return x, y, nil
}
