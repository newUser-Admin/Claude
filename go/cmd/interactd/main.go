// interactd is the server daemon for the interact system.
//
// Usage:
//
//	interactd [flags]
//
// Flags:
//
//	--addr             bind address (default :8080)
//	--db               BoltDB database path (default interact.db)
//	--token            shared secret (CHANGE THIS in production)
//	--tls-cert         TLS certificate file (enables HTTPS/WSS when set)
//	--tls-key          TLS private key file
//	--node-id          stable node identity (default: hostname)
//	--jitter           jitter buffer delay in ms (default 100)
//	--auth-challenge   use challenge-response on WS instead of token-in-URL
//	--sign-frames      HMAC-sign binary wire frames (45 bytes instead of 13)
//	--encrypt-payloads AES-256-GCM encrypt payload data at rest
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
		addr            string
		dbPath          string
		token           string
		tlsCert         string
		tlsKey          string
		nodeID          string
		jitterMs        int
		challengeAuth   bool
		signFrames      bool
		encryptPayloads bool
	)

	cmd := &cobra.Command{
		Use:   "interactd",
		Short: "interact server — WebSocket interaction relay + shell + sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "changeme" {
				fmt.Fprintln(os.Stderr, "[WARN] token is 'changeme' — set --token to a strong secret in production")
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

			cfg := server.Config{
				Token:           token,
				JitterDelay:     time.Duration(jitterMs) * time.Millisecond,
				NodeID:          nodeID,
				ChallengeAuth:   challengeAuth,
				SignFrames:      signFrames,
				EncryptPayloads: encryptPayloads,
			}
			srv := server.New(cfg, st)

			httpSrv := &http.Server{
				Addr:         addr,
				Handler:      srv,
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 0, // long-lived WebSocket connections
				IdleTimeout:  120 * time.Second,
			}

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
			fmt.Printf("[interactd] auth=challenge:%v  sign-frames:%v  encrypt-payloads:%v\n",
				challengeAuth, signFrames, encryptPayloads)
			fmt.Printf("[interactd] endpoints:\n")
			fmt.Printf("  REST  %s/api/interactions\n", addr)
			fmt.Printf("  REST  %s/api/payloads\n", addr)
			fmt.Printf("  WS    %s/ws/interact   (binary interaction relay)\n", addr)
			fmt.Printf("  WS    %s/ws/shell      (PTY — shell-client.html compatible)\n", addr)
			fmt.Printf("  WS    %s/ws/sync       (vector-clock sync)\n", addr)
			fmt.Printf("  GET   %s/health\n", addr)

			if tlsCert != "" && tlsKey != "" {
				return httpSrv.ListenAndServeTLS(tlsCert, tlsKey)
			}
			return httpSrv.ListenAndServe()
		},
	}

	cmd.Flags().StringVar(&addr, "addr", ":8080", "bind address")
	cmd.Flags().StringVar(&dbPath, "db", "interact.db", "BoltDB database path")
	cmd.Flags().StringVar(&token, "token", "changeme", "shared secret token")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate file (enables HTTPS/WSS)")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS private key file")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "stable node identity (default: hostname)")
	cmd.Flags().IntVar(&jitterMs, "jitter", 100, "jitter buffer delay in milliseconds")
	cmd.Flags().BoolVar(&challengeAuth, "auth-challenge", false,
		"use PBKDF2+HMAC challenge-response on WebSocket instead of token-in-URL")
	cmd.Flags().BoolVar(&signFrames, "sign-frames", false,
		"HMAC-SHA256-sign binary wire frames (increases frame size 13→45 bytes)")
	cmd.Flags().BoolVar(&encryptPayloads, "encrypt-payloads", false,
		"AES-256-GCM encrypt payload Data before storing")

	return cmd
}
