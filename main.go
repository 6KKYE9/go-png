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
	"os"
	"strconv"
	"strings"
)

// RGB 是一个 24 位颜色。
type RGB struct {
	R, G, B uint8
}

// Image 是一张 RGB 位图。
type Image struct {
	W, H int
	Pix   []byte // 长度 W*H*3，按行优先 RGB
}

// NewImage 创建一张给定尺寸的黑色图片。
func NewImage(w, h int) *Image {
	return &Image{W: w, H: h, Pix: make([]byte, w*h*3)}
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
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			t := float64(x+y) / float64(im.W+im.H)
			c := lerpRGB(a, b, t)
			im.Set(x, y, c)
		}
	}
}

func lerpRGB(a, b RGB, t float64) RGB {
	return RGB{
		R: uint8(float64(a.R) + t*float64(b.R-a.R)),
		G: uint8(float64(a.G) + t*float64(b.G-a.G)),
		B: uint8(float64(a.B) + t*float64(b.B-a.B)),
	}
}

// Save 把图片编码成 PNG 并写入文件。
func (im *Image) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	// PNG 签名
	w.Write([]byte{137, 80, 78, 71, 13, 10, 26, 10})

	// IHDR
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(im.W))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(im.H))
	ihdr[8] = 8 // 位深
	ihdr[9] = 2 // 颜色类型 2 = Truecolor RGB
	// 10,11,12 = 压缩/滤波/交错，均为 0
	writeChunk(w, "IHDR", ihdr)

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
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	writeChunk(w, "IDAT", buf.Bytes())

	// IEND
	writeChunk(w, "IEND", nil)
	return w.Flush()
}

// writeChunk 写入一个 PNG 数据块（长度+类型+数据+CRC）。
func writeChunk(w *bufio.Writer, typ string, data []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	w.Write(lenBuf[:])
	typeBytes := []byte(typ)
	w.Write(typeBytes)
	w.Write(data)
	crc := crc32.ChecksumIEEE(append(typeBytes, data...))
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], crc)
	w.Write(crcBuf[:])
}

// parseColor 把 "#rrggbb" 或 "rrggbb" 解析成 RGB。
func parseColor(s string) (RGB, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return RGB{}, fmt.Errorf("颜色需为 #rrggbb: %s", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("非法颜色: %s", s)
	}
	return RGB{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}, nil
}

// drawText 在 (x,y) 起始位置用内置 5x7 字体画文字（白字黑底友好）。
func (im *Image) drawText(x, y int, s string, c RGB) {
	col := 0
	for _, r := range s {
		glyph, ok := font5x7[r]
		if !ok {
			glyph = font5x7['?']
		}
		for gy := 0; gy < 7; gy++ {
			bits := glyph[gy]
			for gx := 0; gx < 5; gx++ {
				if bits&(1<<(4-gx)) != 0 {
					im.Set(x+col+gx, y+gy, c)
				}
			}
		}
		col += 6 // 5 宽 + 1 间距
	}
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
  -bg   背景色 #rrggbb
  -grad 渐变两端色（逗号分隔，替代 -bg）
  -rect x0,y0,x1,y1,color  画一个实心矩形
  -text 要绘制的文字
  -fg   文字颜色（默认 #ffffff）
  -at  文字位置 x,y（默认 10,10）
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

	if *out == "" {
		usage()
		os.Exit(1)
	}
	if *w <= 0 || *h <= 0 {
		fmt.Println("宽高必须为正数")
		os.Exit(1)
	}
	im := NewImage(*w, *h)
	if *grad != "" {
		parts := strings.SplitN(*grad, ",", 2)
		if len(parts) != 2 {
			fmt.Println("-grad 需要两端色，如 #ff0000,#0000ff")
			os.Exit(1)
		}
		a, err := parseColor(parts[0])
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		b, err := parseColor(parts[1])
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		im.gradient(a, b)
	} else {
		c, err := parseColor(*bg)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		im.Fill(c)
	}
	if *rect != "" {
		nums, col, err := parseRect(*rect)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		im.Rect(nums[0], nums[1], nums[2], nums[3], col)
	}
	if *text != "" {
		fgc, err := parseColor(*fg)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		px, py := 10, 10
		if atParts := strings.SplitN(*at, ",", 2); len(atParts) == 2 {
			if v, e := strconv.Atoi(strings.TrimSpace(atParts[0])); e == nil {
				px = v
			}
			if v, e := strconv.Atoi(strings.TrimSpace(atParts[1])); e == nil {
				py = v
			}
		}
		im.drawText(px, py, *text, fgc)
	}
	if err := im.Save(*out); err != nil {
		fmt.Println("保存失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s (%dx%d)\n", *out, *w, *h)
}
