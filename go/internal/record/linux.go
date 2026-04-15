//go:build linux

// Linux evdev backend — reads raw keyboard events from /dev/input/eventX
// without CGo or elevated privileges (user must be in the 'input' group).
//
// Auto-detects the keyboard device; override with INTERACT_INPUT_DEV=<path>.
package record

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"context"

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

const (
	evKey     = 0x01 // EV_KEY
	kvPress   = 1
	kvRepeat  = 2
)

func init() {
	SetBackend(&EvdevBackend{})
}

// EvdevBackend captures system-wide keyboard events via Linux evdev.
type EvdevBackend struct {
	// Device overrides auto-detection. Empty means auto-detect.
	Device string
}

func (b *EvdevBackend) Start(ctx context.Context, ch chan<- events.WireEvent) error {
	device := b.Device
	if device == "" {
		if d := os.Getenv("INTERACT_INPUT_DEV"); d != "" {
			device = d
		}
	}
	if device == "" {
		var err error
		device, err = findKeyboardDevice()
		if err != nil {
			return err
		}
	}

	f, err := os.Open(device)
	if err != nil {
		return fmt.Errorf(
			"open %s: %w\n"+
				"  hint: sudo usermod -aG input $USER && newgrp input\n"+
				"  or:   sudo chmod a+r %s",
			device, err, device,
		)
	}
	defer f.Close()

	fmt.Fprintf(os.Stderr, "[evdev] capturing from %s\n", device)

	// Close the file when the context is cancelled to unblock binary.Read.
	go func() {
		<-ctx.Done()
		f.Close()
	}()

	var startMs int64
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

		// Only key-press and key-repeat events.
		if ie.Type != evKey || (ie.Value != kvPress && ie.Value != kvRepeat) {
			continue
		}

		nowMs := ie.Sec*1000 + ie.Usec/1000
		if startMs == 0 {
			startMs = nowMs
		}

		ch <- events.WireEvent{
			Type: events.MsgKey,
			T:    uint32(nowMs - startMs),
			V1:   float32(evdevToChar(ie.Code)),
		}
	}
}

// ─── Device discovery ─────────────────────────────────────────────────────────

func findKeyboardDevice() (string, error) {
	// 1. Look for by-path / by-id symlinks ending in "-kbd".
	for _, base := range []string{"/dev/input/by-path", "/dev/input/by-id"} {
		matches, _ := filepath.Glob(base + "/*-kbd")
		for _, m := range matches {
			if target, err := filepath.EvalSymlinks(m); err == nil {
				return target, nil
			}
		}
	}

	// 2. Parse /proc/bus/input/devices for devices with EV_KEY capability.
	if dev, err := scanProcInputDevices(); err == nil && dev != "" {
		return dev, nil
	}

	return "", fmt.Errorf(
		"no keyboard device found; set INTERACT_INPUT_DEV=/dev/input/eventN",
	)
}

// scanProcInputDevices returns the first /dev/input/eventN that has the EV_KEY
// (0x01) event-type bit set — i.e. it can produce key events.
func scanProcInputDevices() (string, error) {
	data, err := os.ReadFile("/proc/bus/input/devices")
	if err != nil {
		return "", err
	}

	type devInfo struct {
		hasKey   bool
		handlers []string
	}

	var cur devInfo
	var all []devInfo

	flush := func() {
		if cur.hasKey && len(cur.handlers) > 0 {
			all = append(all, cur)
		}
		cur = devInfo{}
	}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flush()

		case strings.HasPrefix(line, "B: EV="):
			hexStr := strings.TrimPrefix(line, "B: EV=")
			if v, err := strconv.ParseUint(hexStr, 16, 64); err == nil {
				cur.hasKey = v&(1<<1) != 0 // EV_KEY is bit 1
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

	// Prefer the device with the most key capabilities (typically the keyboard
	// rather than a mouse or joystick that also has a few key codes).
	if len(all) > 0 {
		return all[0].handlers[0], nil
	}
	return "", nil
}

// ─── Keycode → character mapping (US QWERTY, unshifted) ──────────────────────

// evdevToChar converts a Linux evdev key code to its representative rune.
// Unmapped codes are returned as-is so they round-trip without loss.
func evdevToChar(code uint16) rune {
	if int(code) < len(evdevMap) {
		if r := evdevMap[code]; r != 0 {
			return r
		}
	}
	return rune(code)
}

// evdevMap is indexed by evdev key code (0–127).
// Values are the unshifted US-QWERTY characters.
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
