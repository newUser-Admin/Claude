//go:build windows

// Windows system-wide keyboard and mouse capture via WH_KEYBOARD_LL and
// WH_MOUSE_LL (low-level hooks).
//
// Both hooks are installed on the same OS thread and driven by a single Win32
// message loop so the goroutine is locked to its OS thread for the session.
// No elevated privileges are required.
package record

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/newuser-admin/claude/interact/pkg/events"
)

// Win32 constants.
const (
	whKeyboardLL = 13
	whMouseLL    = 14

	wmKeyDown    = 0x0100
	wmSyskeyDown = 0x0104
	wmLButtonDown = 0x0201
	wmRButtonDown = 0x0204
	wmMButtonDown = 0x0207
	wmMouseWheel  = 0x020A
	wmMouseHWheel = 0x020E

	smCxScreen = 0
	smCyScreen = 1
)

// KBDLLHOOKSTRUCT is the lParam for WH_KEYBOARD_LL.
type kbdllHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// MSLLHOOKSTRUCT is the lParam for WH_MOUSE_LL.
type msllHookStruct struct {
	PtX         int32
	PtY         int32
	MouseData   uint32 // high word = wheel delta; low word = button
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	setWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	callNextHookEx      = user32.NewProc("CallNextHookEx")
	unhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	getMessage          = user32.NewProc("GetMessageW")
	translateMessage    = user32.NewProc("TranslateMessage")
	dispatchMessageW    = user32.NewProc("DispatchMessageW")
	postQuitMessage     = user32.NewProc("PostQuitMessage")
	getSystemMetrics    = user32.NewProc("GetSystemMetrics")
)

var hookState struct {
	mu        sync.Mutex
	ch        chan<- events.WireEvent
	startMs   int64
	kbdHook   uintptr
	mouseHook uintptr
	sw, sh    int32 // screen dimensions for click normalisation
	active    int32 // atomic
}

func init() {
	SetBackend(&WinHookBackend{})
}

// WinHookBackend captures system-wide keyboard and mouse events.
type WinHookBackend struct{}

func (b *WinHookBackend) Start(ctx context.Context, ch chan<- events.WireEvent) error {
	sw, _, _ := getSystemMetrics.Call(smCxScreen)
	sh, _, _ := getSystemMetrics.Call(smCyScreen)

	hookState.mu.Lock()
	hookState.ch = ch
	hookState.startMs = 0
	hookState.sw = int32(sw)
	hookState.sh = int32(sh)
	hookState.mu.Unlock()
	atomic.StoreInt32(&hookState.active, 1)

	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		kbdCb := windows.NewCallback(kbdHookProc)
		kbdHook, _, err := setWindowsHookExW.Call(whKeyboardLL, kbdCb, 0, 0)
		if kbdHook == 0 {
			errCh <- fmt.Errorf("SetWindowsHookEx(keyboard): %w", err)
			return
		}

		mouseCb := windows.NewCallback(mouseHookProc)
		mouseHook, _, _ := setWindowsHookExW.Call(whMouseLL, mouseCb, 0, 0)
		// Mouse hook failure is non-fatal.

		hookState.mu.Lock()
		hookState.kbdHook = kbdHook
		hookState.mouseHook = mouseHook
		hookState.mu.Unlock()

		fmt.Fprintln(os.Stderr, "[winhook] capturing system-wide keyboard+mouse events")
		errCh <- nil

		type msg struct {
			hwnd    uintptr
			message uint32
			wParam  uintptr
			lParam  uintptr
			time    uint32
			pt      [2]int32
		}
		var m msg
		for atomic.LoadInt32(&hookState.active) == 1 {
			ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if ret == 0 {
				break
			}
			translateMessage.Call(uintptr(unsafe.Pointer(&m)))
			dispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}

		if mouseHook != 0 {
			unhookWindowsHookEx.Call(mouseHook)
		}
		unhookWindowsHookEx.Call(kbdHook)
	}()

	if err := <-errCh; err != nil {
		return err
	}

	<-ctx.Done()
	atomic.StoreInt32(&hookState.active, 0)

	hookState.mu.Lock()
	hookState.ch = nil
	hookState.mu.Unlock()

	postQuitMessage.Call(0)
	return nil
}

// ─── Hook callbacks ───────────────────────────────────────────────────────────

func nowAndStart() (uint32, int64) {
	now := time.Now().UnixMilli()
	hookState.mu.Lock()
	if hookState.startMs == 0 {
		hookState.startMs = now
	}
	start := hookState.startMs
	hookState.mu.Unlock()
	return uint32(now - start), start
}

func kbdHookProc(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode < 0 {
		ret, _, _ := callNextHookEx.Call(hookState.kbdHook, uintptr(nCode), wParam, lParam)
		return ret
	}
	if wParam == wmKeyDown || wParam == wmSyskeyDown {
		ks := (*kbdllHookStruct)(unsafe.Pointer(lParam))
		hookState.mu.Lock()
		ch := hookState.ch
		hookState.mu.Unlock()
		if ch != nil {
			t, _ := nowAndStart()
			select {
			case ch <- events.WireEvent{
				Type: events.MsgKey,
				T:    t,
				V1:   float32(winVKToChar(ks.VkCode)),
			}:
			default:
			}
		}
	}
	ret, _, _ := callNextHookEx.Call(hookState.kbdHook, uintptr(nCode), wParam, lParam)
	return ret
}

func mouseHookProc(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode < 0 {
		ret, _, _ := callNextHookEx.Call(hookState.mouseHook, uintptr(nCode), wParam, lParam)
		return ret
	}

	ms := (*msllHookStruct)(unsafe.Pointer(lParam))
	hookState.mu.Lock()
	ch := hookState.ch
	sw := hookState.sw
	sh := hookState.sh
	hookState.mu.Unlock()

	if ch != nil {
		t, _ := nowAndStart()
		switch wParam {
		case wmLButtonDown, wmRButtonDown, wmMButtonDown:
			var btn float32
			switch wParam {
			case wmLButtonDown:
				btn = 0
			case wmRButtonDown:
				btn = 1
			default:
				btn = 2
			}
			var xFrac, yFrac float32
			if sw > 0 && sh > 0 {
				xFrac = clamp01(float32(ms.PtX) / float32(sw))
				yFrac = clamp01(float32(ms.PtY) / float32(sh))
			}
			select {
			case ch <- events.WireEvent{
				Type: events.MsgClick,
				T:    t,
				V1:   xFrac + btn*1000,
				V2:   yFrac,
			}:
			default:
			}

		case wmMouseWheel:
			delta := int16(ms.MouseData >> 16)
			select {
			case ch <- events.WireEvent{
				Type: events.MsgScroll,
				T:    t,
				V1:   0,
				V2:   float32(delta) / 120,
			}:
			default:
			}

		case wmMouseHWheel:
			delta := int16(ms.MouseData >> 16)
			select {
			case ch <- events.WireEvent{
				Type: events.MsgScroll,
				T:    t,
				V1:   float32(delta) / 120,
				V2:   0,
			}:
			default:
			}
		}
	}

	ret, _, _ := callNextHookEx.Call(hookState.mouseHook, uintptr(nCode), wParam, lParam)
	return ret
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ─── Windows virtual key → character (US QWERTY, unshifted) ─────────────────

func winVKToChar(vk uint32) rune {
	if int(vk) < len(winVKMap) {
		if r := winVKMap[vk]; r != 0 {
			return r
		}
	}
	return rune(vk)
}

var winVKMap = [256]rune{
	0x08: '\b',
	0x09: '\t',
	0x0D: '\n',
	0x20: ' ',
	0x30: '0', 0x31: '1', 0x32: '2', 0x33: '3', 0x34: '4',
	0x35: '5', 0x36: '6', 0x37: '7', 0x38: '8', 0x39: '9',
	0x41: 'a', 0x42: 'b', 0x43: 'c', 0x44: 'd', 0x45: 'e',
	0x46: 'f', 0x47: 'g', 0x48: 'h', 0x49: 'i', 0x4A: 'j',
	0x4B: 'k', 0x4C: 'l', 0x4D: 'm', 0x4E: 'n', 0x4F: 'o',
	0x50: 'p', 0x51: 'q', 0x52: 'r', 0x53: 's', 0x54: 't',
	0x55: 'u', 0x56: 'v', 0x57: 'w', 0x58: 'x', 0x59: 'y',
	0x5A: 'z',
	0xBA: ';', 0xBB: '=', 0xBC: ',', 0xBD: '-', 0xBE: '.', 0xBF: '/',
	0xC0: '`', 0xDB: '[', 0xDC: '\\', 0xDD: ']', 0xDE: '\'',
}
