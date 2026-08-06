# go-png

PNG 编码自己写（IHDR/IEND/IDAT 块、CRC、像素封装全手写），IDAT 用标准库 `compress/zlib` 压缩。

功能：

- 纯色背景 / 线性渐变背景
- 画实心矩形
- 用内置 5x7 像素字体绘制文字

```bash
# 纯色背景
go-png -out a.png -w 200 -h 100 -bg #202020

# 渐变背景
go-png -out b.png -w 200 -h 100 -grad #ff0000,#0000ff

# 画矩形
go-png -out c.png -w 200 -h 100 -bg #222 -rect 10,10,80,60,#00ff00

# 画文字
go-png -out d.png -w 200 -h 100 -bg #222 -text "Hi 1" -fg #ffffff -at 20,20
```

选项：

| 选项 | 说明 | 默认 |
|------|------|------|
| `-out` | 输出文件路径（必填） | 无 |
| `-w` | 宽度 | 200 |
| `-h` | 高度 | 100 |
| `-bg` | 背景色 `#rrggbb` | `#202020` |
| `-grad` | 渐变两端色（逗号分隔） | 无 |
| `-rect` | 矩形 `x0,y0,x1,y1,color` | 无 |
| `-text` | 文字内容 | 无 |
| `-fg` | 文字颜色 | `#ffffff` |
| `-at` | 文字位置 `x,y` | `10,10` |

测试覆盖像素读写、填充、矩形、颜色解析、PNG 文件签名校验、文字绘制、zlib 压缩往返（用标准库 `compress/zlib` 验证）。

几点说明：

- 输出为 8 位真彩色（RGB）PNG，浏览器与系统图片查看器均可直接打开
- 字体仅覆盖常用 ASCII（字母/数字/标点），缺字回退到 `?`
- IDAT 走标准库 `compress/zlib` 压缩，比原来的无压缩存储块体积小很多
