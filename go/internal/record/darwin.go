//go:build darwin

// macOS system-wide keyboard capture via CGEventTap (CoreGraphics).
//
// Prerequisites:
//   - Grant "Accessibility" permission to the binary in
//     System Preferences → Privacy & Security → Accessibility.
//   - If permission is missing CGEventTapCreate returns nil and an
//     informative error is returned.
package record

// #cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
//
// #include <ApplicationServices/ApplicationServices.h>
// #include <stdlib.h>
//
// // Forward declaration of the Go callback defined below.
// extern CGEventRef goKeyCallback(CGEventTapProxy, CGEventType, CGEventRef, void*);
//
// // startTap creates a session-level event tap for key-down events and starts
// // running the run-loop source.  Returns NULL on failure (no Accessibility perms).
// static CFMachPortRef startTap(void) {
//     CGEventMask mask = CGEventMaskBit(kCGEventKeyDown) |
//                        CGEventMaskBit(kCGEventKeyUp);
//     CFMachPortRef tap = CGEventTapCreate(
//         kCGSessionEventTap,
//         kCGHeadInsertEventTap,
//         kCGEventTapOptionListenOnly,
//         mask,
//         goKeyCallback,
//         NULL);
//     if (tap == NULL) return NULL;
//
//     CFRunLoopSourceRef src = CFMachPortCreateRunLoopSource(NULL, tap, 0);
//     CFRunLoopAddSource(CFRunLoopGetCurrent(), src, kCFRunLoopCommonModes);
//     CGEventTapEnable(tap, true);
//     CFRelease(src);
//     return tap;
// }
//
// static void stopTap(CFMachPortRef tap) {
//     CGEventTapEnable(tap, false);
//     CFRelease(tap);
// }
//
// static void runLoopRunShort(void) {
//     // Pump the run loop for up to 50 ms, then return so Go can check ctx.
//     CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.05, false);
// }
import "C"

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/newuser-admin/claude/interact/pkg/events"
)

// tapState holds the channel for the currently active tap.
// CGEventTap callbacks are C-level so we use a global to reach Go state.
var tapState struct {
	mu      sync.Mutex
	ch      chan<- events.WireEvent
	startMs int64
}

//export goKeyCallback
func goKeyCallback(proxy C.CGEventTapProxy, typ C.CGEventType, event C.CGEventRef, _ unsafe.Pointer) C.CGEventRef {
	tapState.mu.Lock()
	ch := tapState.ch
	start := tapState.startMs
	tapState.mu.Unlock()

	if ch == nil {
		return event
	}

	if typ != C.kCGEventKeyDown {
		return event
	}

	keyCode := C.CGEventGetIntegerValueField(event, C.kCGKeyboardEventKeycode)

	nowMs := time.Now().UnixMilli()
	if start == 0 {
		tapState.mu.Lock()
		tapState.startMs = nowMs
		tapState.mu.Unlock()
		start = nowMs
	}

	select {
	case ch <- events.WireEvent{
		Type: events.MsgKey,
		T:    uint32(nowMs - start),
		V1:   float32(macKeyToChar(uint16(keyCode))),
	}:
	default:
	}
	return event
}

func init() {
	SetBackend(&CGEventBackend{})
}

// CGEventBackend captures system-wide keyboard events using CGEventTap.
type CGEventBackend struct{}

func (b *CGEventBackend) Start(ctx context.Context, ch chan<- events.WireEvent) error {
	tap := C.startTap()
	if tap == nil {
		return fmt.Errorf(
			"CGEventTapCreate failed — grant Accessibility permission to this binary in\n" +
				"  System Preferences → Privacy & Security → Accessibility",
		)
	}
	defer C.stopTap(tap)

	tapState.mu.Lock()
	tapState.ch = ch
	tapState.startMs = 0
	tapState.mu.Unlock()

	defer func() {
		tapState.mu.Lock()
		tapState.ch = nil
		tapState.mu.Unlock()
	}()

	fmt.Fprintln(os.Stderr, "[cgeventtap] capturing system-wide keyboard events")

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			C.runLoopRunShort()
		}
	}
}

// ─── macOS virtual key code → character (US QWERTY) ─────────────────────────

func macKeyToChar(vk uint16) rune {
	if int(vk) < len(macKeyMap) {
		if r := macKeyMap[vk]; r != 0 {
			return r
		}
	}
	return rune(vk)
}

// macKeyMap maps macOS virtual key codes to unshifted US-QWERTY characters.
// Source: <HIToolbox/Events.h> kVK_* constants.
var macKeyMap = [128]rune{
	0:  'a',
	1:  's',
	2:  'd',
	3:  'f',
	4:  'h',
	5:  'g',
	6:  'z',
	7:  'x',
	8:  'c',
	9:  'v',
	11: 'b',
	12: 'q',
	13: 'w',
	14: 'e',
	15: 'r',
	16: 'y',
	17: 't',
	18: '1',
	19: '2',
	20: '3',
	21: '4',
	22: '6',
	23: '5',
	24: '=',
	25: '9',
	26: '7',
	27: '-',
	28: '8',
	29: '0',
	30: ']',
	31: 'o',
	32: 'u',
	33: '[',
	34: 'i',
	35: 'p',
	36: '\n',
	37: 'l',
	38: 'j',
	39: '\'',
	40: 'k',
	41: ';',
	42: '\\',
	43: ',',
	44: '/',
	45: 'n',
	46: 'm',
	47: '.',
	48: '\t',
	49: ' ',
	51: '\b',
}
