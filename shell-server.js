/**
 * WEBSOCKET SHELL SERVER
 *
 * Spawns a real PTY per connection and bridges it over a WebSocket.
 * Each client gets an isolated shell process; disconnecting kills it.
 *
 * Dependencies:
 *   npm install ws node-pty
 *
 * Usage:
 *   node shell-server.js
 *
 * Environment variables:
 *   PORT         – listening port          (default: 8080)
 *   HOST         – bind address            (default: 127.0.0.1  ← localhost only)
 *   SHELL_TOKEN  – shared-secret token     (default: changeme — CHANGE THIS)
 *   SHELL        – shell binary to spawn   (default: $SHELL or /bin/bash)
 *
 * Wire protocol (JSON over WebSocket text frames):
 *   Client → Server
 *     { type: "input",  data: "<string>" }
 *     { type: "resize", cols: <n>, rows: <n> }
 *
 *   Server → Client
 *     { type: "output", data: "<string>" }   – terminal output (may include ANSI)
 *     { type: "exit",   code: <n> }          – shell process exited
 *     { type: "error",  message: "<string>" } – server-side error
 *
 * SECURITY NOTICE
 *   This server grants full shell access to anyone holding the token.
 *   - Never expose it on a public interface (keep HOST=127.0.0.1).
 *   - Replace SHELL_TOKEN with a long random secret in production.
 *   - Run as an unprivileged user.
 */

'use strict';

const { WebSocketServer } = require('ws');
const pty                 = require('node-pty');
const os                  = require('os');

// ─── Configuration ────────────────────────────────────────────
const PORT         = parseInt(process.env.PORT  || '8080', 10);
const HOST         = process.env.HOST        || '127.0.0.1';
const TOKEN        = process.env.SHELL_TOKEN || 'changeme';
const SHELL_BIN    = process.env.SHELL       || (os.platform() === 'win32' ? 'cmd.exe' : '/bin/bash');
const DEFAULT_COLS = 80;
const DEFAULT_ROWS = 24;

if (TOKEN === 'changeme') {
    console.warn('[WARN] SHELL_TOKEN is set to the default "changeme". Set a strong secret via the SHELL_TOKEN env var.');
}

// ─── Server ───────────────────────────────────────────────────
const wss = new WebSocketServer({ host: HOST, port: PORT });

wss.on('listening', () => {
    console.log(`[Shell] WebSocket server listening on ws://${HOST}:${PORT}`);
    console.log(`[Shell] Shell binary : ${SHELL_BIN}`);
    console.log(`[Shell] Token auth   : enabled (SHELL_TOKEN)`);
});

wss.on('connection', (ws, req) => {
    // ── Token authentication via ?token= query parameter ──────
    let url;
    try {
        url = new URL(req.url, `http://${req.headers.host ?? 'localhost'}`);
    } catch {
        ws.close(4000, 'Bad request');
        return;
    }

    if (url.searchParams.get('token') !== TOKEN) {
        console.warn(`[Shell] Rejected connection from ${req.socket.remoteAddress} — bad token`);
        ws.close(4001, 'Unauthorized');
        return;
    }

    const remote = req.socket.remoteAddress;
    console.log(`[Shell] Client connected: ${remote}`);

    // ── Spawn PTY ─────────────────────────────────────────────
    let ptyProc;
    try {
        ptyProc = pty.spawn(SHELL_BIN, [], {
            name: 'xterm-256color',
            cols: DEFAULT_COLS,
            rows: DEFAULT_ROWS,
            cwd:  os.homedir(),
            env:  process.env,
        });
    } catch (err) {
        console.error('[Shell] Failed to spawn PTY:', err.message);
        safeSend(ws, { type: 'error', message: `Failed to spawn shell: ${err.message}` });
        ws.close();
        return;
    }

    console.log(`[Shell] Spawned PID ${ptyProc.pid} (${SHELL_BIN})`);

    // ── PTY → WebSocket ───────────────────────────────────────
    ptyProc.onData((data) => {
        safeSend(ws, { type: 'output', data });
    });

    ptyProc.onExit(({ exitCode, signal }) => {
        console.log(`[Shell] PID ${ptyProc.pid} exited (code=${exitCode}, signal=${signal})`);
        safeSend(ws, { type: 'exit', code: exitCode });
        ws.close();
    });

    // ── WebSocket → PTY ───────────────────────────────────────
    ws.on('message', (raw) => {
        let msg;
        try {
            msg = JSON.parse(raw);
        } catch {
            return; // ignore malformed frames
        }

        switch (msg.type) {
            case 'input':
                if (typeof msg.data === 'string') {
                    ptyProc.write(msg.data);
                }
                break;

            case 'resize': {
                const cols = Math.max(1, parseInt(msg.cols, 10) || DEFAULT_COLS);
                const rows = Math.max(1, parseInt(msg.rows, 10) || DEFAULT_ROWS);
                ptyProc.resize(cols, rows);
                break;
            }

            default:
                // Unknown message types are silently ignored.
        }
    });

    // ── Cleanup on disconnect ─────────────────────────────────
    ws.on('close', () => {
        console.log(`[Shell] Client disconnected: ${remote} — killing PID ${ptyProc.pid}`);
        try { ptyProc.kill(); } catch { /* already dead */ }
    });

    ws.on('error', (err) => {
        console.error(`[Shell] WebSocket error (${remote}):`, err.message);
    });
});

wss.on('error', (err) => {
    console.error('[Shell] Server error:', err.message);
    process.exit(1);
});

// ── Graceful shutdown ─────────────────────────────────────────
function shutdown(signal) {
    console.log(`\n[Shell] Received ${signal}, shutting down…`);
    wss.close(() => process.exit(0));
}
process.on('SIGINT',  () => shutdown('SIGINT'));
process.on('SIGTERM', () => shutdown('SIGTERM'));

// ── Helpers ───────────────────────────────────────────────────
function safeSend(ws, obj) {
    if (ws.readyState === ws.OPEN) {
        try { ws.send(JSON.stringify(obj)); } catch { /* client gone */ }
    }
}
