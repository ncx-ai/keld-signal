//go:build ignore

// Regenerates app/src-tauri/icons/icon.ico from the PNGs render-icon.mjs
// produces. Run from the repo root:
//
//	go run app/src-tauri/icons/src/make-ico.go
//
// ⚠️ WHY THIS IS NOT PART OF render-icon.mjs. That script renders the mark in a
// headless browser and screenshots it, which is how the app icon cannot drift
// from the one the page uses — but a browser cannot emit an .ico. This packs the
// PNGs it already wrote; the artwork still has exactly one source.
//
// ⚠️ WHY AN .ico EXISTS AT ALL. tauri-build REQUIRES icons/icon.ico on Windows to
// generate the executable's resource section, and fails the build without it —
// "`icons/icon.ico` not found; required for generating a Windows Resource file".
// The app had only ever been built for macOS, so nothing had needed one.
//
// ⚠️ 256 IS THE CEILING, AND icon.png IS 512. An ICONDIRENTRY stores width and
// height in ONE BYTE each, with 0 meaning 256 — so a 512px entry cannot be
// expressed and is downscaled rather than embedded. The 32 and 128 entries use
// the natively-rendered PNGs instead of downscales: the mark is inset inside a
// rounded plate, and rendering at the target size keeps that geometry crisp in
// a way resampling from 512 does not.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
)

// entry is one image inside the .ico.
type entry struct {
	size int
	png  []byte
}

func main() {
	dir := filepath.Join("app", "src-tauri", "icons")
	if _, err := os.Stat(dir); err != nil {
		fatal("run this from the repo root: %v", err)
	}

	// Native renders first, so they win over a downscale at the same size.
	native := map[int]string{32: "32x32.png", 128: "128x128.png"}
	master := filepath.Join(dir, "icon.png")

	src, err := load(master)
	if err != nil {
		fatal("read %s: %v", master, err)
	}

	var entries []entry
	for _, size := range []int{16, 32, 48, 64, 128, 256} {
		if name, ok := native[size]; ok {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				entries = append(entries, entry{size, raw})
				continue
			}
			// Fall through to a downscale rather than dropping the size.
			fmt.Fprintf(os.Stderr, "note: %s unreadable (%v); downscaling instead\n", name, err)
		}
		buf, err := scaleToPNG(src, size)
		if err != nil {
			fatal("scale to %d: %v", size, err)
		}
		entries = append(entries, entry{size, buf})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size < entries[j].size })

	out := filepath.Join(dir, "icon.ico")
	if err := os.WriteFile(out, buildICO(entries), 0o644); err != nil {
		fatal("write %s: %v", out, err)
	}
	fmt.Printf("wrote %s with %d entries:", out, len(entries))
	for _, e := range entries {
		fmt.Printf(" %d", e.size)
	}
	fmt.Println()
}

func load(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// scaleToPNG area-averages src down to size×size.
//
// ⚠️ HAND-ROLLED RATHER THAN golang.org/x/image/draw, DELIBERATELY. That would
// be a new dependency in the shipping module's go.mod for a build-time
// generator that runs perhaps twice a year — the wrong trade. Area averaging is
// the right filter here anyway: every ratio is a large reduction (512→256 down
// to 512→16), which is exactly where box/area beats the interpolating kernels,
// and the two sizes where a sharper filter would matter (32, 128) come from
// native renders and never reach this function.
//
// ⚠️ AVERAGED IN PREMULTIPLIED ALPHA. The plate is opaque and the surround fully
// transparent, so averaging straight RGBA would pull the transparent pixels'
// colour into the edge and leave a dark halo. image.RGBA is already
// premultiplied, which is what makes the naive sum correct.
func scaleToPNG(src image.Image, size int) ([]byte, error) {
	b := src.Bounds()
	// Work from a premultiplied RGBA copy so the arithmetic below is valid
	// whatever concrete type the decoder handed back.
	rgba := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			rgba.Set(x, y, src.At(x, y))
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	sw, sh := b.Dx(), b.Dy()
	for dy := 0; dy < size; dy++ {
		y0, y1 := dy*sh/size, (dy+1)*sh/size
		if y1 == y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < size; dx++ {
			x0, x1 := dx*sw/size, (dx+1)*sw/size
			if x1 == x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					c := rgba.RGBAAt(b.Min.X+x, b.Min.Y+y)
					r += uint64(c.R)
					g += uint64(c.G)
					bl += uint64(c.B)
					a += uint64(c.A)
					n++
				}
			}
			dst.SetRGBA(dx, dy, color.RGBA{
				R: uint8(r / n), G: uint8(g / n), B: uint8(bl / n), A: uint8(a / n),
			})
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// buildICO packs PNG-compressed entries, which Windows Vista and later read
// directly — no BMP/DIB conversion, no AND mask to get wrong.
func buildICO(entries []entry) []byte {
	var b bytes.Buffer
	// ICONDIR: reserved, type=1 (icon), count.
	binary.Write(&b, binary.LittleEndian, uint16(0))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(len(entries)))

	// Image data starts after the directory and all its entries.
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		dim := byte(e.size)
		if e.size >= 256 {
			dim = 0 // 0 means 256; 256 does not fit in a byte.
		}
		b.WriteByte(dim)                                          // width
		b.WriteByte(dim)                                          // height
		b.WriteByte(0)                                            // colours in palette (0 = truecolour)
		b.WriteByte(0)                                            // reserved
		binary.Write(&b, binary.LittleEndian, uint16(1))          // colour planes
		binary.Write(&b, binary.LittleEndian, uint16(32))         // bits per pixel
		binary.Write(&b, binary.LittleEndian, uint32(len(e.png))) // bytes in resource
		binary.Write(&b, binary.LittleEndian, uint32(offset))     // offset
		offset += len(e.png)
	}
	for _, e := range entries {
		b.Write(e.png)
	}
	return b.Bytes()
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "make-ico: "+format+"\n", a...)
	os.Exit(1)
}
