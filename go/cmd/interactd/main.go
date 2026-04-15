// interactd is the server daemon for the interact system.
//
// Usage:
//
//	interactd [flags]
//
// Flags:
//
//	--addr    bind address (default :8080)
//	--db      BoltDB database path (default interact.db)
//	--token   shared secret (default: changeme — CHANGE THIS)
//	--tls-cert  TLS certificate file (enables HTTPS/WSS when set)
//	--tls-key   TLS private key file
//	--node-id   stable node identity (default: hostname)
//	--jitter    jitter buffer delay in ms (default 100)
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/newuser-admin/claude/interact/internal/server"
	"github.com/newuser-admin/claude/interact/internal/store"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var (
		addr    string
		dbPath  string
		token   string
		tlsCert string
		tlsKey  string
		nodeID  string
		jitterMs int
	)

	cmd := &cobra.Command{
		Use:   "interactd",
		Short: "interact server — WebSocket interaction relay + shell + sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "changeme" {
				fmt.Fprintln(os.Stderr, "[WARN] using default token 'changeme' — set --token in production")
			}

			if nodeID == "" {
				host, _ := os.Hostname()
				nodeID = host
			}

			st, err := store.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer st.Close()

			srv := server.New(server.Config{
				Token:       token,
				JitterDelay: time.Duration(jitterMs) * time.Millisecond,
				NodeID:      nodeID,
			}, st)

			httpSrv := &http.Server{
				Addr:         addr,
				Handler:      srv,
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 0, // WebSocket connections are long-lived
				IdleTimeout:  120 * time.Second,
			}

			// Graceful shutdown on SIGINT / SIGTERM.
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-quit
				fmt.Println("\n[interactd] shutting down…")
				httpSrv.Close()
			}()

			proto := "ws"
			if tlsCert != "" {
				proto = "wss"
			}
			fmt.Printf("[interactd] listening on %s://%s  node=%s\n", proto, addr, nodeID)
			fmt.Printf("[interactd] endpoints:\n")
			fmt.Printf("  REST  %s/api/interactions\n", addr)
			fmt.Printf("  REST  %s/api/payloads\n", addr)
			fmt.Printf("  WS    %s/ws/interact?token=...\n", addr)
			fmt.Printf("  WS    %s/ws/shell?token=...   (shell-client.html compatible)\n", addr)
			fmt.Printf("  WS    %s/ws/sync?token=...\n", addr)

			if tlsCert != "" && tlsKey != "" {
				return httpSrv.ListenAndServeTLS(tlsCert, tlsKey)
			}
			return httpSrv.ListenAndServe()
		},
	}

	cmd.Flags().StringVar(&addr, "addr", ":8080", "bind address")
	cmd.Flags().StringVar(&dbPath, "db", "interact.db", "BoltDB database path")
	cmd.Flags().StringVar(&token, "token", "changeme", "shared secret token")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate file (enables HTTPS)")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS key file")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "stable node identity (default: hostname)")
	cmd.Flags().IntVar(&jitterMs, "jitter", 100, "jitter buffer delay in milliseconds")

	return cmd
}
