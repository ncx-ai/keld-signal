// Stands in for the Inno wizard: a top-level window with a child STATIC panel
// playing the part of a custom page's TPanel. It spawns the host with that
// panel's HWND — exactly what Inno's `Exec(..., IntToStr(Panel.Handle), ...)`
// does — then captures the window to a PNG.
//
// ⚠️ THE SCREENSHOT IS THE VERDICT, not the log. `Embed` returning true only
// says a call succeeded; a PNG showing the page inside the panel says a browser
// surface actually rendered in a window this process owns. The macOS work was
// wrong twice about "it worked" on exactly this kind of evidence.
//
// Usage: harness.exe <hostExe> <url> <logpath> <pngpath>
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pCreateWindowExW = user32.NewProc("CreateWindowExW")
	pGetMessageW     = user32.NewProc("GetMessageW")
	pTranslateMsg    = user32.NewProc("TranslateMessage")
	pDispatchMsgW    = user32.NewProc("DispatchMessageW")
	// ⚠️ PostThreadMessageW, NOT PostQuitMessage. PostQuitMessage posts WM_QUIT
	// to the CALLING thread's queue, and the timer below runs on a different
	// OS thread — so the loop never saw it and the probe hung until the job
	// timed out. The quit has to be addressed to the loop's own thread id.
	pPostThreadMsgW  = user32.NewProc("PostThreadMessageW")
	pGetCurrentThrd  = kernel32.NewProc("GetCurrentThreadId")
	pUpdateWindow    = user32.NewProc("UpdateWindow")
	pGetClientRect   = user32.NewProc("GetClientRect")
	pGetWindowDC     = user32.NewProc("GetWindowDC")
	pReleaseDC       = user32.NewProc("ReleaseDC")
	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	srcCopy            = 0x00CC0020
	biRGB              = 0
	dibRGBColors       = 0
	wmQuit             = 0x0012
)

type rect struct{ Left, Top, Right, Bottom int32 }

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type bitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// capture BitBlts hwnd into a PNG. Top-down DIB (negative height) so the rows
// arrive in image order, and BGRA -> RGBA on the way out.
func capture(hwnd uintptr, path string) error {
	var r rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w <= 0 || h <= 0 {
		return fmt.Errorf("window client rect is %dx%d", w, h)
	}
	srcDC, _, _ := pGetWindowDC.Call(hwnd)
	if srcDC == 0 {
		return fmt.Errorf("GetWindowDC failed")
	}
	defer pReleaseDC.Call(hwnd, srcDC)
	memDC, _, _ := pCreateCompatibleDC.Call(srcDC)
	if memDC == 0 {
		return fmt.Errorf("CreateCompatibleDC failed")
	}
	defer pDeleteDC.Call(memDC)

	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width: int32(w), Height: int32(-h),
		Planes: 1, BitCount: 32, Compression: biRGB,
	}}
	var bits unsafe.Pointer
	bmp, _, _ := pCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bi)),
		dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		return fmt.Errorf("CreateDIBSection failed")
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(memDC, bmp)
	defer pSelectObject.Call(memDC, old)

	ok, _, _ := pBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), srcDC, 0, 0, srcCopy)
	if ok == 0 {
		return fmt.Errorf("BitBlt failed")
	}

	src := unsafe.Slice((*byte)(bits), w*h*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b, g, rr, a := src[i*4], src[i*4+1], src[i*4+2], src[i*4+3]
		if a == 0 {
			a = 255 // BitBlt leaves alpha zero; a fully transparent PNG proves nothing
		}
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = rr, g, b, a
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func main() {
	runtime.LockOSThread()
	mainThread, _, _ := pGetCurrentThrd.Call()
	// A wedged message loop must not burn the job timeout: the first hang cost
	// 15 minutes and produced no log at all.
	time.AfterFunc(75*time.Second, func() { fmt.Println("WATCHDOG: forcing exit"); os.Exit(3) })

	if len(os.Args) < 5 {
		fmt.Println("usage: harness <hostExe> <url> <logpath> <pngpath>")
		os.Exit(2)
	}
	hostExe, url, logPath, pngPath := os.Args[1], os.Args[2], os.Args[3], os.Args[4]

	hinst, _, _ := pGetModuleHandle.Call(0)
	// "#32770" is the predefined dialog class — the closest stand-in for a
	// wizard window. What matters is the child panel below it.
	cls, _ := syscall.UTF16PtrFromString("#32770")
	title, _ := syscall.UTF16PtrFromString("Keld wizard probe harness")
	top, _, e := pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsVisible, 80, 80, 760, 560, 0, 0, hinst, 0)
	if top == 0 {
		fmt.Println("FAIL top-level CreateWindowExW:", e)
		os.Exit(1)
	}
	panelCls, _ := syscall.UTF16PtrFromString("STATIC")
	empty, _ := syscall.UTF16PtrFromString("")
	panel, _, e2 := pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(panelCls)), uintptr(unsafe.Pointer(empty)),
		wsChild|wsVisible, 12, 48, 716, 452, top, 0, hinst, 0)
	if panel == 0 {
		fmt.Println("FAIL panel CreateWindowExW:", e2)
		os.Exit(1)
	}
	pUpdateWindow.Call(top)
	fmt.Printf("harness pid=%d top=%d panel=%d\n", os.Getpid(), top, panel)

	cmd := exec.Command(hostExe, strconv.FormatUint(uint64(panel), 10), url, logPath, "8")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("FAIL starting host:", err)
		os.Exit(1)
	}
	fmt.Printf("host started pid=%d\n", cmd.Process.Pid)

	go func() {
		// After the host has had time to create, embed and navigate.
		time.Sleep(14 * time.Second)
		if err := capture(top, pngPath); err != nil {
			fmt.Println("capture failed:", err)
		} else {
			fmt.Println("captured", pngPath)
		}
		time.Sleep(2 * time.Second)
		pPostThreadMsgW.Call(mainThread, wmQuit, 0, 0)
	}()

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMsg.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMsgW.Call(uintptr(unsafe.Pointer(&m)))
	}
	_ = cmd.Wait()
	fmt.Println("harness done")
}
