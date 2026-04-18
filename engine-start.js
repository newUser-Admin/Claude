#!/usr/bin/env node
/**
 * engine-start.js
 *
 * Standalone process for the Interaction Engine.
 * Runs a WebSocket server; all connected clients share one engine instance.
 * Binary frames from any client are passed through the engine and broadcast
 * back to every other live connection.
 *
 * Environment variables:
 *   PORT       — WebSocket port     (default: 8079)
 *   HOST       — bind address       (default: 127.0.0.1)
 *   LOG_LEVEL  — 'quiet' | 'info'   (default: 'info')
 *
 * Requires Node.js >= 18 (globalThis.performance, dynamic import, ESM).
 */

'use strict';

const http              = require('http');
const path              = require('path');
const { WebSocketServer } = require('ws');
const { pathToFileURL } = require('url');

const HOST    = process.env.HOST      ?? '127.0.0.1';
const PORT    = parseInt(process.env.PORT ?? '8079', 10);
const VERBOSE = process.env.LOG_LEVEL !== 'quiet';

const ts  = () => new Date().toISOString();
const log = (...a) => VERBOSE && process.stdout.write(`${ts()} [engine] ${a.join(' ')}\n`);
const err = (...a) =>            process.stderr.write(`${ts()} [engine:err] ${a.join(' ')}\n`);

async function main() {
  // Load the ES module; pathToFileURL keeps Windows paths valid.
  const moduleUrl = pathToFileURL(
    path.resolve(__dirname, 'interaction-engine.js')
  ).href;
  const { InteractionEngine, BinarySerializer } = await import(moduleUrl);

  // One shared engine — memory persistence, fast 20 ms tick.
  const engine = new InteractionEngine({
    persistence: 'memory',
    storageKey:  'server',
    tickRate:    20,
  });

  // All live WebSocket connections.
  const clients = new Set();

  // Broadcast a binary frame to every connected client except the sender.
  function broadcast(frame, exclude = null) {
    for (const ws of clients) {
      if (ws !== exclude && ws.readyState === ws.OPEN) ws.send(frame);
    }
  }

  engine
    .on('update', (state) => {
      // Notify all clients of the new merged state.
      const frame = BinarySerializer.encode({ type: 'state', state, ts: Date.now() });
      broadcast(frame);
    })
    .on('error', (e) => err('decode error:', e.message))
    .on('connect', ()    => log('engine connected'))
    .on('disconnect', () => log('engine disconnected'));

  // Wire the engine's outbound send to the broadcast channel.
  engine.connect((frame) => broadcast(frame));

  // ── HTTP + WebSocket server ──────────────────────────────────────────────
  const server = http.createServer((_req, res) => {
    const diag = JSON.stringify(engine.diagnostics(), null, 2);
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(diag);
  });

  const wss = new WebSocketServer({ server });

  wss.on('connection', (ws, req) => {
    clients.add(ws);
    log(`+ client  total=${clients.size}  addr=${req.socket.remoteAddress}`);

    // Send the current snapshot so the client starts in sync.
    try {
      const snap = BinarySerializer.encode({
        type:  'state',
        state: engine.getState(),
        ts:    Date.now(),
      });
      ws.send(snap);
    } catch (e) {
      err('snapshot send failed:', e.message);
    }

    ws.on('message', (data) => {
      try {
        engine.receive(data instanceof Buffer ? new Uint8Array(data.buffer, data.byteOffset, data.byteLength) : data);
      } catch (e) {
        err('message handler:', e.message);
      }
    });

    ws.on('close', (code, reason) => {
      clients.delete(ws);
      log(`- client  total=${clients.size}  code=${code} reason=${reason}`);
    });

    ws.on('error', (e) => err('ws client:', e.message));
  });

  server.listen(PORT, HOST, () =>
    log(`listening on ws://${HOST}:${PORT}  (HTTP diagnostics on same port)`)
  );

  // ── Graceful shutdown ────────────────────────────────────────────────────
  function shutdown(signal) {
    log(`${signal} — shutting down`);
    engine.persist();
    engine.disconnect();

    // Stop accepting new connections, then close HTTP.
    wss.close(() =>
      server.close(() => {
        log('stopped cleanly');
        process.exit(0);
      })
    );

    // Force-exit if clean shutdown stalls beyond 6 s.
    setTimeout(() => { err('force-exit after timeout'); process.exit(1); }, 6000).unref();
  }

  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT',  () => shutdown('SIGINT'));

  process.on('uncaughtException',  (e) => { err('uncaughtException:', e.stack); });
  process.on('unhandledRejection', (r) => { err('unhandledRejection:', r);      });
}

main().catch((e) => { err('fatal:', e.stack); process.exit(1); });
