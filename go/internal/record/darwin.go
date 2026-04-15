//go:build darwin

// macOS system-wide keyboard, mouse-click and scroll capture via CGEventTap.
//
// Prerequisites:
//   - Grant "Accessibility" permission to the binary in
//     System Preferences → Privacy & Security → Accessibility.
//   - If permission is missing CGEventTapCreate returns nil and an
//     informative error is returned to the caller.
package record

// #cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
//
// #include <ApplicationServices/ApplicationServices.h>
// #include <stdlib.h>
//
// // Forward declaration of the Go callback.
// extern CGEventRef goEventCallback(CGEventTapProxy, CGEventType, CGEventRef, void*);
//
// // startTap creates a session-level event tap for key-down, left/right mouse-down
// // and scroll-wheel events, then adds it to the current CFRunLoop.
// static CFMachPortRef startTap(void) {
//     CGEventMask mask =
//         CGEventMaskBit(kCGEventKeyDown)          |
//         CGEventMaskBit(kCGEventLeftMouseDown)    |
//         CGEventMaskBit(kCGEventRightMouseDown)   |
//         CGEventMaskBit(kCGEventOtherMouseDown)   |
//         CGEventMaskBit(kCGEventScrollWheel);
//
//     CFMachPortRef tap = CGEventTapCreate(
//         kCGSessionEventTap,
//         kCGHeadInsertEventTap,
//         kCGEventTapOptionListenOnly,
//         mask,
//         goEventCallback,
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
// // Pump the run loop for up to 50 ms.
// static void runLoopRunShort(void) {
//     CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.05, false);
// }
//
// // Screen dimensions for normalising click coordinates.
// static void screenSize(CGFloat *w, CGFloat *h) {
//     CGDirectDisplayID d = CGMainDisplayID();
//     *w = (CGFloat)CGDisplayPixelsWide(d);
//     *h = (CGFloat)CGDisplayPixelsHigh(d);
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
var tapState struct {
	mu      sync.Mutex
	ch      chan<- events.WireEvent
	startMs int64
	sw, sh  float64 // screen dimensions
}

//export goEventCallback
func goEventCallback(_ C.CGEventTapProxy, typ C.CGEventType, event C.CGEventRef, _ unsafe.Pointer) C.CGEventRef {
	tapState.mu.Lock()
	ch := tapState.ch
	start := tapState.startMs
	sw := tapState.sw
	sh := tapState.sh
	tapState.mu.Unlock()

	if ch == nil {
		return event
	}

	nowMs := time.Now().UnixMilli()
	if start == 0 {
		tapState.mu.Lock()
		tapState.startMs = nowMs
		tapState.mu.Unlock()
		start = nowMs
	}
	t := uint32(nowMs - start)

	switch typ {
	case C.kCGEventKeyDown:
		keyCode := C.CGEventGetIntegerValueField(event, C.kCGKeyboardEventKeycode)
		select {
		case ch <- events.WireEvent{
			Type: events.MsgKey,
			T:    t,
			V1:   float32(macKeyToChar(uint16(keyCode))),
		}:
		default:
		}

	case C.kCGEventLeftMouseDown, C.kCGEventRightMouseDown, C.kCGEventOtherMouseDown:
		var btn float32
		switch typ {
		case C.kCGEventLeftMouseDown:
			btn = 0
		case C.kCGEventRightMouseDown:
			btn = 1
		default:
			btn = 2
		}
		loc := C.CGEventGetLocation(event)
		var xFrac, yFrac float32
		if sw > 0 && sh > 0 {
			xFrac = float32(float64(loc.x) / sw)
			yFrac = float32(float64(loc.y) / sh)
		}
		// Encode button index above the 0-1 fraction range (same convention as Linux).
		select {
		case ch <- events.WireEvent{
			Type: events.MsgClick,
			T:    t,
			V1:   xFrac + btn*1000,
			V2:   yFrac,
		}:
		default:
		}

	case C.kCGEventScrollWheel:
		dx := C.CGEventGetIntegerValueField(event, C.kCGScrollWheelEventDeltaAxis2)
		dy := C.CGEventGetIntegerValueField(event, C.kCGScrollWheelEventDeltaAxis1)
		if dx == 0 && dy == 0 {
			break
		}
		select {
		case ch <- events.WireEvent{
			Type: events.MsgScroll,
			T:    t,
			V1:   float32(dx),
			V2:   float32(dy),
		}:
		default:
		}
	}

	return event
}

func init() {
	SetBackend(&CGEventBackend{})
}

// CGEventBackend captures system-wide events using CGEventTap.
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

	var sw, sh C.CGFloat
	C.screenSize(&sw, &sh)

	tapState.mu.Lock()
	tapState.ch = ch
	tapState.startMs = 0
	tapState.sw = float64(sw)
	tapState.sh = float64(sh)
	tapState.mu.Unlock()

	defer func() {
		tapState.mu.Lock()
		tapState.ch = nil
		tapState.mu.Unlock()
	}()

	fmt.Fprintf(os.Stderr, "[cgeventtap] capturing keyboard+mouse (%.0fx%.0f)\n", float64(sw), float64(sh))

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
