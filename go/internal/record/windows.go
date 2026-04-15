//go:build windows

// Windows system-wide keyboard capture via WH_KEYBOARD_LL (low-level hook).
//
// WH_KEYBOARD_LL fires for every key event on the system, regardless of which
// window has focus, without requiring elevated privileges.
//
// The hook is installed on the current OS thread and driven by a Win32 message
// loop so the goroutine is locked to the thread for the lifetime of the session.
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

const (
	whKeyboardLL = 13
	wmKeyDown    = 0x0100
	wmSyskeyDown = 0x0104
)

// KBDLLHOOKSTRUCT is the structure passed to a low-level keyboard hook proc.
type kbdllHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
)

// hookState is shared between the Win32 callback and the goroutine.
var hookState struct {
	mu      sync.Mutex
	ch      chan<- events.WireEvent
	startMs int64
	hook    uintptr
	active  int32 // atomic
}

func init() {
	SetBackend(&WinHookBackend{})
}

// WinHookBackend captures system-wide keyboard events using WH_KEYBOARD_LL.
type WinHookBackend struct{}

func (b *WinHookBackend) Start(ctx context.Context, ch chan<- events.WireEvent) error {
	hookState.mu.Lock()
	hookState.ch = ch
	hookState.startMs = 0
	hookState.mu.Unlock()
	atomic.StoreInt32(&hookState.active, 1)

	// The hook must be installed and pumped on the same OS thread.
	errCh := make(chan error, 1)
	go func() {
		// Lock this goroutine to its OS thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		cb := windows.NewCallback(lowLevelKeyboardProc)
		hook, _, err := procSetWindowsHookExW.Call(
			whKeyboardLL,
			cb,
			0,
			0,
		)
		if hook == 0 {
			errCh <- fmt.Errorf("SetWindowsHookEx: %w", err)
			return
		}
		hookState.mu.Lock()
		hookState.hook = hook
		hookState.mu.Unlock()

		fmt.Fprintln(os.Stderr, "[winhook] capturing system-wide keyboard events")
		errCh <- nil // signal success

		// Message loop — required to deliver hook messages.
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
			ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if ret == 0 { // WM_QUIT
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}

		procUnhookWindowsHookEx.Call(hook)
	}()

	if err := <-errCh; err != nil {
		return err
	}

	// Wait for context cancellation, then stop the hook.
	<-ctx.Done()
	atomic.StoreInt32(&hookState.active, 0)

	hookState.mu.Lock()
	hookState.ch = nil
	hookState.mu.Unlock()

	// Post WM_QUIT to unblock GetMessage.
	procPostQuitMessage.Call(0)
	return nil
}

// lowLevelKeyboardProc is the Win32 hook callback.
func lowLevelKeyboardProc(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode < 0 {
		ret, _, _ := procCallNextHookEx.Call(hookState.hook, uintptr(nCode), wParam, lParam)
		return ret
	}

	if wParam == wmKeyDown || wParam == wmSyskeyDown {
		ks := (*kbdllHookStruct)(unsafe.Pointer(lParam))

		hookState.mu.Lock()
		ch := hookState.ch
		start := hookState.startMs
		hookState.mu.Unlock()

		if ch != nil {
			nowMs := time.Now().UnixMilli()
			if start == 0 {
				hookState.mu.Lock()
				hookState.startMs = nowMs
				hookState.mu.Unlock()
				start = nowMs
			}
			select {
			case ch <- events.WireEvent{
				Type: events.MsgKey,
				T:    uint32(nowMs - start),
				V1:   float32(winVKToChar(ks.VkCode)),
			}:
			default:
			}
		}
	}

	ret, _, _ := procCallNextHookEx.Call(hookState.hook, uintptr(nCode), wParam, lParam)
	return ret
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

// winVKMap maps Windows virtual key codes to unshifted US-QWERTY characters.
var winVKMap = [256]rune{
	0x08: '\b',
	0x09: '\t',
	0x0D: '\n',
	0x20: ' ',
	// 0–9
	0x30: '0', 0x31: '1', 0x32: '2', 0x33: '3', 0x34: '4',
	0x35: '5', 0x36: '6', 0x37: '7', 0x38: '8', 0x39: '9',
	// A–Z
	0x41: 'a', 0x42: 'b', 0x43: 'c', 0x44: 'd', 0x45: 'e',
	0x46: 'f', 0x47: 'g', 0x48: 'h', 0x49: 'i', 0x4A: 'j',
	0x4B: 'k', 0x4C: 'l', 0x4D: 'm', 0x4E: 'n', 0x4F: 'o',
	0x50: 'p', 0x51: 'q', 0x52: 'r', 0x53: 's', 0x54: 't',
	0x55: 'u', 0x56: 'v', 0x57: 'w', 0x58: 'x', 0x59: 'y',
	0x5A: 'z',
	// OEM keys
	0xBA: ';', 0xBB: '=', 0xBC: ',', 0xBD: '-', 0xBE: '.', 0xBF: '/',
	0xC0: '`', 0xDB: '[', 0xDC: '\\', 0xDD: ']', 0xDE: '\'',
}
