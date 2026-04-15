//go:build linux

// Linux evdev backend — reads raw keyboard AND mouse events from /dev/input/eventX
// without CGo or elevated privileges (user must be in the 'input' group).
//
// Auto-detects the keyboard device; override with INTERACT_INPUT_DEV=<path>.
// Auto-detects the mouse device;    override with INTERACT_MOUSE_DEV=<path>.
// Screen resolution for click normalisation: INTERACT_SCREEN=WxH (default 1920x1080).
package record

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/newuser-admin/claude/interact/pkg/events"
)

// inputEvent mirrors struct input_event from <linux/input.h> on 64-bit Linux.
// Layout: sec(8) + usec(8) + type(2) + code(2) + value(4) = 24 bytes.
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

// Linux event-type constants.
const (
	evKey = 0x01 // EV_KEY
	evRel = 0x02 // EV_REL
	evSyn = 0x00 // EV_SYN

	kvPress  = 1
	kvRepeat = 2

	// EV_REL axis codes
	relX     = 0x00
	relY     = 0x01
	relHWheel = 0x06
	relWheel = 0x08

	// EV_KEY button codes (mouse)
	btnLeft   = 0x110 // 272
	btnRight  = 0x111 // 273
	btnMiddle = 0x112 // 274
)

func init() {
	SetBackend(&EvdevBackend{})
}

// EvdevBackend captures system-wide keyboard and mouse events via Linux evdev.
type EvdevBackend struct {
	// Device overrides auto-detection. Empty means auto-detect.
	Device string
	// MouseDevice overrides mouse auto-detection. Empty means auto-detect.
	MouseDevice string
}

func (b *EvdevBackend) Start(ctx context.Context, ch chan<- events.WireEvent) error {
	kbd := b.Device
	if kbd == "" {
		if d := os.Getenv("INTERACT_INPUT_DEV"); d != "" {
			kbd = d
		}
	}
	mouse := b.MouseDevice
	if mouse == "" {
		if d := os.Getenv("INTERACT_MOUSE_DEV"); d != "" {
			mouse = d
		}
	}

	if kbd == "" {
		var err error
		kbd, err = findDeviceByCapability(evKey, false /* not mouse */)
		if err != nil {
			return err
		}
	}
	if mouse == "" {
		// Best-effort; mouse capture is optional.
		mouse, _ = findDeviceByCapability(evKey, true /* mouse */)
	}

	// Parse screen resolution for click normalisation.
	sw, sh := screenDims()

	f, err := os.Open(kbd)
	if err != nil {
		return fmt.Errorf(
			"open %s: %w\n"+
				"  hint: sudo usermod -aG input $USER && newgrp input\n"+
				"  or:   sudo chmod a+r %s",
			kbd, err, kbd,
		)
	}

	fmt.Fprintf(os.Stderr, "[evdev] keyboard: %s\n", kbd)

	var (
		startMs    int64
		startOnce  sync.Once
		startMsVal atomic.Int64
	)
	ts := func(sec, usec int64) uint32 {
		now := sec*1000 + usec/1000
		startOnce.Do(func() { startMsVal.Store(now) })
		return uint32(now - startMsVal.Load())
	}

	// Context close → close both files.
	go func() { <-ctx.Done(); f.Close() }()

	// Mouse goroutine.
	if mouse != "" {
		mf, err := os.Open(mouse)
		if err == nil {
			fmt.Fprintf(os.Stderr, "[evdev] mouse:    %s\n", mouse)
			go func() {
				defer mf.Close()
				go func() { <-ctx.Done(); mf.Close() }()

				var (
					accX, accY int32 // accumulated relative position (pixels)
				)
				for {
					var ie inputEvent
					if err := binary.Read(mf, binary.LittleEndian, &ie); err != nil {
						return
					}
					t := ts(ie.Sec, ie.Usec)
					switch ie.Type {
					case evRel:
						switch ie.Code {
						case relX:
							accX += ie.Value
						case relY:
							accY += ie.Value
						case relWheel:
							select {
							case ch <- events.WireEvent{
								Type: events.MsgScroll,
								T:    t,
								V1:   0,
								V2:   float32(ie.Value),
							}:
							default:
							}
						case relHWheel:
							select {
							case ch <- events.WireEvent{
								Type: events.MsgScroll,
								T:    t,
								V1:   float32(ie.Value),
								V2:   0,
							}:
							default:
							}
						}
					case evKey:
						if ie.Value != kvPress {
							continue
						}
						var btn float32
						switch ie.Code {
						case btnLeft:
							btn = 0
						case btnRight:
							btn = 1
						case btnMiddle:
							btn = 2
						default:
							continue
						}
						xFrac := clamp01(float32(accX) / float32(sw))
						yFrac := clamp01(float32(accY) / float32(sh))
						select {
						case ch <- events.WireEvent{
							Type: events.MsgClick,
							T:    t,
							V1:   xFrac + btn*1000, // encode button index above 1.0
							V2:   yFrac,
						}:
						default:
						}
					}
				}
			}()
		}
	}
	_ = startMs // keep var alive (used via closure)

	// Keyboard read loop (blocking).
	for {
		var ie inputEvent
		if err := binary.Read(f, binary.LittleEndian, &ie); err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("evdev read: %w", err)
			}
		}
		if ie.Type != evKey || (ie.Value != kvPress && ie.Value != kvRepeat) {
			continue
		}
		t := ts(ie.Sec, ie.Usec)
		ch <- events.WireEvent{
			Type: events.MsgKey,
			T:    t,
			V1:   float32(evdevToChar(ie.Code)),
		}
	}
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

// ─── Screen dimensions ────────────────────────────────────────────────────────

func screenDims() (w, h int) {
	if s := os.Getenv("INTERACT_SCREEN"); s != "" {
		parts := strings.SplitN(s, "x", 2)
		if len(parts) == 2 {
			if pw, err := strconv.Atoi(parts[0]); err == nil {
				if ph, err := strconv.Atoi(parts[1]); err == nil {
					return pw, ph
				}
			}
		}
	}
	// Try to read from /sys/class/drm (best-effort).
	if dw, dh, ok := sysdrm(); ok {
		return dw, dh
	}
	return 1920, 1080
}

func sysdrm() (w, h int, ok bool) {
	matches, _ := filepath.Glob("/sys/class/drm/*/modes")
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		first := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
		// format: "1920x1080"
		parts := strings.SplitN(first, "x", 2)
		if len(parts) != 2 {
			continue
		}
		if pw, err := strconv.Atoi(parts[0]); err == nil {
			if ph, err := strconv.Atoi(parts[1]); err == nil {
				return pw, ph, true
			}
		}
	}
	return 0, 0, false
}

// ─── Device discovery ─────────────────────────────────────────────────────────

// findDeviceByCapability returns a keyboard device (wantMouse=false) or a
// pointer/mouse device (wantMouse=true) by scanning /dev/input/by-{path,id}
// and falling back to /proc/bus/input/devices.
func findDeviceByCapability(capBit uint16, wantMouse bool) (string, error) {
	suffix := "-kbd"
	if wantMouse {
		suffix = "-mouse"
	}
	for _, base := range []string{"/dev/input/by-path", "/dev/input/by-id"} {
		matches, _ := filepath.Glob(base + "/*" + suffix)
		for _, m := range matches {
			if target, err := filepath.EvalSymlinks(m); err == nil {
				return target, nil
			}
		}
	}

	// Fallback: parse /proc/bus/input/devices.
	if wantMouse {
		dev, err := scanForMouse()
		if err == nil && dev != "" {
			return dev, nil
		}
		return "", fmt.Errorf("no mouse device found; set INTERACT_MOUSE_DEV=/dev/input/eventN")
	}
	dev, err := scanProcInputDevices()
	if err == nil && dev != "" {
		return dev, nil
	}
	return "", fmt.Errorf("no keyboard device found; set INTERACT_INPUT_DEV=/dev/input/eventN")
}

// scanProcInputDevices returns the first /dev/input/eventN that has EV_KEY
// (bit 1) set and is not a mouse (no REL axis / BTN_MOUSE).
func scanProcInputDevices() (string, error) {
	return parseProcDevices(false)
}

func scanForMouse() (string, error) {
	return parseProcDevices(true)
}

func parseProcDevices(wantMouse bool) (string, error) {
	data, err := os.ReadFile("/proc/bus/input/devices")
	if err != nil {
		return "", err
	}

	type entry struct {
		hasKey   bool
		hasRel   bool // EV_REL — relative axes (typical of mice)
		handlers []string
	}

	var cur entry
	var all []entry
	flush := func() {
		if len(cur.handlers) > 0 {
			all = append(all, cur)
		}
		cur = entry{}
	}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "B: EV="):
			if v, err := strconv.ParseUint(strings.TrimPrefix(line, "B: EV="), 16, 64); err == nil {
				cur.hasKey = v&(1<<1) != 0
				cur.hasRel = v&(1<<2) != 0
			}
		case strings.HasPrefix(line, "H: Handlers="):
			for _, tok := range strings.Fields(strings.TrimPrefix(line, "H: Handlers=")) {
				if strings.HasPrefix(tok, "event") {
					cur.handlers = append(cur.handlers, "/dev/input/"+tok)
				}
			}
		}
	}
	flush()

	for _, e := range all {
		if wantMouse && e.hasKey && e.hasRel {
			return e.handlers[0], nil
		}
		if !wantMouse && e.hasKey && !e.hasRel {
			return e.handlers[0], nil
		}
	}
	return "", nil
}

// ─── Keycode → character mapping (US QWERTY, unshifted) ──────────────────────

func evdevToChar(code uint16) rune {
	if int(code) < len(evdevMap) {
		if r := evdevMap[code]; r != 0 {
			return r
		}
	}
	return rune(code)
}

// evdevMap is indexed by evdev key code (0–127).
var evdevMap = [128]rune{
	// 0-13
	0, 0x1b, '1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '-', '=',
	// 14-27 (backspace, tab, q..])
	'\b', '\t', 'q', 'w', 'e', 'r', 't', 'y', 'u', 'i', 'o', 'p', '[', ']',
	// 28-41 (enter, ctrl, a..#)
	'\n', 0, 'a', 's', 'd', 'f', 'g', 'h', 'j', 'k', 'l', ';', '\'', '`',
	// 42-54 (lshift, \, z../)
	0, '\\', 'z', 'x', 'c', 'v', 'b', 'n', 'm', ',', '.', '/',
	// 55-56 (rshift modifier, kp*)
	0, '*',
	// 57 (space)
	57: ' ',
}
