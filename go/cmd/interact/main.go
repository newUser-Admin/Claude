// interact is the command-line client for the interact system.
//
// Commands:
//
//	interact record [name]            – record keys from stdin; save to server
//	interact play   <id>              – stream an interaction from server and replay locally
//	interact list                     – list interactions on the server
//	interact delete <id>              – delete an interaction
//	interact push   <file> [--kind]   – push a local file as a payload to the server
//	interact pull   [--pending]       – list (and optionally download) server payloads
//	interact ack    <payload-id>      – acknowledge a payload
//	interact sync                     – run a one-shot vector-clock sync session
//	interact shell                    – open an interactive PTY shell to the server
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/newuser-admin/claude/interact/internal/playback"
	"github.com/newuser-admin/claude/interact/internal/record"
	isync "github.com/newuser-admin/claude/interact/internal/sync"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ─── Global flags ────────────────────────────────────────────────────────────

var (
	serverURL string
	token     string
)

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "interact",
		Short: "interact — multi-platform interaction recorder, player, and remote sync client",
	}
	root.PersistentFlags().StringVar(&serverURL, "server", "http://localhost:8080", "server base URL")
	root.PersistentFlags().StringVar(&token, "token", "changeme", "shared secret token")

	root.AddCommand(
		cmdRecord(),
		cmdPlay(),
		cmdList(),
		cmdDelete(),
		cmdPush(),
		cmdPull(),
		cmdAck(),
		cmdSync(),
		cmdShell(),
	)
	return root
}

// ─── record ──────────────────────────────────────────────────────────────────

func cmdRecord() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "record [name]",
		Short: "Record keystrokes from stdin and upload to the server",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				name = fmt.Sprintf("rec-%s", time.Now().Format("20060102-150405"))
			}

			// Open WebSocket in record mode.
			wsURL := wsEndpoint("/ws/interact") + "&mode=record&name=" + url.QueryEscape(name)
			ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return fmt.Errorf("connect: %w", err)
			}
			defer ws.Close()

			fmt.Fprintf(os.Stderr, "[interact] recording %q — Ctrl-C to stop\n", name)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			rec := record.New()
			ch := rec.Subscribe()

			// Fan captured events to the WebSocket.
			go func() {
				for ev := range ch {
					b := events.Pack(ev)
					ws.WriteMessage(websocket.BinaryMessage, b[:]) //nolint:errcheck
				}
			}()

			return rec.Record(ctx)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "interaction name (default: rec-<timestamp>)")
	return cmd
}

// ─── play ────────────────────────────────────────────────────────────────────

func cmdPlay() *cobra.Command {
	var speed float64
	cmd := &cobra.Command{
		Use:   "play <id>",
		Short: "Stream an interaction from the server and replay locally",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			wsURL := wsEndpoint("/ws/interact") + "&mode=play&id=" + url.QueryEscape(id)
			ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return fmt.Errorf("connect: %w", err)
			}
			defer ws.Close()

			fmt.Fprintf(os.Stderr, "[interact] playing %s at %.1fx speed\n", id, speed)

			player := playback.New(nil)
			player.Speed = speed

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			var evs []events.WireEvent
			for {
				_, data, err := ws.ReadMessage()
				if err != nil {
					break
				}
				ev, ok := events.UnpackSlice(data)
				if !ok {
					continue
				}
				evs = append(evs, ev)
			}

			if len(evs) == 0 {
				fmt.Fprintln(os.Stderr, "[interact] no events received")
				return nil
			}

			return player.PlaySync(ctx, evs)
		},
	}
	cmd.Flags().Float64Var(&speed, "speed", 1.0, "playback speed multiplier")
	return cmd
}

// ─── list ────────────────────────────────────────────────────────────────────

func cmdList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List interactions stored on the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			var list []*events.Interaction
			if err := apiGet("/api/interactions", &list); err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("(no interactions)")
				return nil
			}
			fmt.Printf("%-36s  %-30s  %s\n", "ID", "NAME", "UPDATED")
			for _, ia := range list {
				fmt.Printf("%-36s  %-30s  %s  (%d events)\n",
					ia.ID, ia.Name, ia.UpdatedAt.Format(time.RFC3339), len(ia.Events))
			}
			return nil
		},
	}
}

// ─── delete ──────────────────────────────────────────────────────────────────

func cmdDelete() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an interaction from the server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiDelete("/api/interactions/" + args[0])
		},
	}
}

// ─── push ────────────────────────────────────────────────────────────────────

func cmdPush() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "push <file>",
		Short: "Push a local file as a payload to the server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var kindCode events.PayloadKind
			switch kind {
			case "config":
				kindCode = events.PayloadConfig
			case "event-stream":
				kindCode = events.PayloadEventStream
			case "patch":
				kindCode = events.PayloadPatch
			default:
				kindCode = events.PayloadConfig
			}
			p := events.Payload{
				Kind: kindCode,
				Name: args[0],
				Data: data,
			}
			var out events.Payload
			return apiPost("/api/payloads", p, &out)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "config", "payload kind: config | event-stream | patch")
	return cmd
}

// ─── pull ────────────────────────────────────────────────────────────────────

func cmdPull() *cobra.Command {
	var pending bool
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "List payloads available on the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/payloads"
			if pending {
				path += "?pending=1"
			}
			var list []*events.Payload
			if err := apiGet(path, &list); err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("(no payloads)")
				return nil
			}
			fmt.Printf("%-36s  %-8s  %-30s  %s\n", "ID", "KIND", "NAME", "ACKED")
			for _, p := range list {
				fmt.Printf("%-36s  %-8s  %-30s  %v\n", p.ID, p.Kind.String(), p.Name, p.Acked)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&pending, "pending", false, "only show un-acked payloads")
	return cmd
}

// ─── ack ─────────────────────────────────────────────────────────────────────

func cmdAck() *cobra.Command {
	return &cobra.Command{
		Use:   "ack <payload-id>",
		Short: "Acknowledge a payload on the server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/api/payloads/"+args[0]+"/ack", nil, nil)
		},
	}
}

// ─── sync ────────────────────────────────────────────────────────────────────

func cmdSync() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Run a one-shot vector-clock sync session with the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Local in-memory sync engine (stateless client)
			eng := isync.New("", nil)

			wsURL := wsEndpoint("/ws/sync")
			ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return fmt.Errorf("connect: %w", err)
			}
			defer ws.Close()

			fmt.Fprintln(os.Stderr, "[sync] session started")

			// Receive server Hello.
			_, raw, err := ws.ReadMessage()
			if err != nil {
				return err
			}
			serverHello, err := isync.UnmarshalHello(raw)
			if err != nil {
				return fmt.Errorf("parse server hello: %w", err)
			}
			eng.Merge(serverHello.VectorClock)
			fmt.Fprintf(os.Stderr, "[sync] server node=%s  interactions=%d\n",
				serverHello.NodeID, len(serverHello.Known))

			// Send our (empty) Hello.
			clientHello, _ := eng.Hello()
			helloBytes, _ := isync.MarshalHello(clientHello)
			if err := ws.WriteMessage(websocket.TextMessage, helloBytes); err != nil {
				return err
			}

			// Receive ops from server and print them.
			ops := 0
			for {
				_, raw, err := ws.ReadMessage()
				if err != nil {
					break
				}
				if string(raw) == "{}" {
					break
				}
				op, err := isync.UnmarshalOp(raw)
				if err != nil {
					continue
				}
				ops++
				switch op.Op {
				case "upsert":
					if op.Interaction != nil {
						fmt.Printf("[sync] + %s  %q  (%d events)\n",
							op.Interaction.ID, op.Interaction.Name, len(op.Interaction.Events))
						if dbPath != "" {
							// TODO: persist to local store when --db is set
							_ = dbPath
						}
					}
				case "delete":
					fmt.Printf("[sync] - %s\n", op.DeleteID)
				}
			}
			// Send end sentinel.
			ws.WriteMessage(websocket.TextMessage, []byte("{}")) //nolint:errcheck

			fmt.Fprintf(os.Stderr, "[sync] done — %d ops received\n", ops)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "local BoltDB path to persist synced interactions")
	return cmd
}

// ─── shell ───────────────────────────────────────────────────────────────────

func cmdShell() *cobra.Command {
	return &cobra.Command{
		Use:   "shell",
		Short: "Open an interactive PTY shell session on the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			wsURL := wsEndpoint("/ws/shell")
			ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return fmt.Errorf("connect: %w", err)
			}
			defer ws.Close()

			fmt.Fprintf(os.Stderr, "[shell] connected to %s\n", serverURL)

			fd := int(os.Stdin.Fd())
			oldState, err := term.MakeRaw(fd)
			if err != nil {
				return fmt.Errorf("raw mode: %w", err)
			}
			defer term.Restore(fd, oldState)

			// Send initial resize.
			cols, rows, _ := term.GetSize(fd)
			sendShellMsg(ws, events.ShellMsg{Type: "resize", Cols: cols, Rows: rows})

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			// Stdin → WebSocket
			go func() {
				defer cancel()
				scanner := bufio.NewReader(os.Stdin)
				buf := make([]byte, 256)
				for {
					n, err := scanner.Read(buf)
					if n > 0 {
						sendShellMsg(ws, events.ShellMsg{Type: "input", Data: string(buf[:n])})
					}
					if err != nil {
						return
					}
					select {
					case <-ctx.Done():
						return
					default:
					}
				}
			}()

			// WebSocket → Stdout
			for {
				select {
				case <-ctx.Done():
					return nil
				default:
				}
				_, raw, err := ws.ReadMessage()
				if err != nil {
					return nil
				}
				var msg events.ShellMsg
				if json.Unmarshal(raw, &msg) != nil {
					continue
				}
				switch msg.Type {
				case "output":
					os.Stdout.WriteString(msg.Data)
				case "exit":
					fmt.Fprintf(os.Stderr, "\r\n[shell] exited (code %d)\r\n", msg.Code)
					return nil
				case "error":
					fmt.Fprintf(os.Stderr, "\r\n[shell] error: %s\r\n", msg.Message)
				}
			}
		},
	}
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func apiGet(path string, out interface{}) error {
	req, _ := http.NewRequest(http.MethodGet, serverURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func apiPost(path string, body interface{}, out interface{}) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = strings.NewReader(string(data))
	}
	req, _ := http.NewRequest(http.MethodPost, serverURL+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func apiDelete(path string) error {
	req, _ := http.NewRequest(http.MethodDelete, serverURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	fmt.Printf("deleted\n")
	return nil
}

// wsEndpoint converts the HTTP server URL to a WebSocket URL for the given
// path and appends the auth token.
func wsEndpoint(path string) string {
	u := serverURL
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + path + "?token=" + url.QueryEscape(token)
}

func sendShellMsg(ws *websocket.Conn, msg events.ShellMsg) {
	data, _ := json.Marshal(msg)
	ws.WriteMessage(websocket.TextMessage, data) //nolint:errcheck
}
