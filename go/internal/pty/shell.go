// Package pty bridges a real PTY process over a WebSocket connection using the
// same JSON wire protocol as shell-server.js, so the existing shell-client.html
// works without modification.
//
// Build constraints: PTY spawning is supported on Linux, macOS, and any Unix
// platform that creack/pty handles.  Windows support requires ConPTY (a
// separate build tag; not included here).
package pty

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

const (
	defaultCols = 80
	defaultRows = 24
)

// Handler handles a single WebSocket shell session, matching the behaviour of
// the wss.on('connection') block in shell-server.js.
func Handler(ws *websocket.Conn, remote string) {
	shell := shellBin()
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: defaultCols,
		Rows: defaultRows,
	})
	if err != nil {
		sendMsg(ws, events.ShellMsg{Type: "error", Message: fmt.Sprintf("spawn failed: %v", err)})
		ws.Close()
		return
	}
	defer func() {
		ptmx.Close()
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
	}()

	fmt.Printf("[pty] spawned PID %d (%s) for %s\n", cmd.Process.Pid, shell, remote)

	// PTY → WebSocket: stream terminal output.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				sendMsg(ws, events.ShellMsg{Type: "output", Data: string(buf[:n])})
			}
			if err != nil {
				break
			}
		}
		sendMsg(ws, events.ShellMsg{Type: "exit", Code: 0})
		ws.Close()
	}()

	// WebSocket → PTY: relay client input.
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			break
		}
		var msg events.ShellMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			ptmx.Write([]byte(msg.Data)) //nolint:errcheck
		case "resize":
			cols, rows := msg.Cols, msg.Rows
			if cols <= 0 {
				cols = defaultCols
			}
			if rows <= 0 {
				rows = defaultRows
			}
			pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint:errcheck
		}
	}

	fmt.Printf("[pty] client disconnected: %s — killing PID %d\n", remote, cmd.Process.Pid)
}

func shellBin() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "/bin/bash"
}

func sendMsg(ws *websocket.Conn, msg events.ShellMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	ws.WriteMessage(websocket.TextMessage, data) //nolint:errcheck
}
