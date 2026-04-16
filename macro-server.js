'use strict';

/**
 * macro-server.js — Encrypted macro storage server
 *
 * Routes:
 *   GET    /macros          List stored macros (metadata only)
 *   POST   /macros          Store a new macro (events[] + name)
 *   GET    /macros/:name    Retrieve & decrypt a macro for playback
 *   DELETE /macros/:name    Delete a macro
 *
 * Security:
 *   • Bearer-style token auth via X-Macro-Token header or ?token= query param
 *   • AES-256-GCM encryption; key derived from this machine's system fingerprint
 *     via PBKDF2 (100 000 iterations) — macros are unreadable on any other host
 *   • Macro names are validated to [a-zA-Z0-9_-] to prevent path traversal
 *
 * Environment variables:
 *   MACRO_PORT   (default 3000)
 *   MACRO_HOST   (default 127.0.0.1)
 *   MACRO_TOKEN  (default "changeme" — change in production)
 */

const express = require('express');
const crypto  = require('crypto');
const os      = require('os');
const fs      = require('fs');
const path    = require('path');

// ── System fingerprint ────────────────────────────────────────────────────────
// Combines stable, hardware-bound identifiers into a deterministic fingerprint.
// Same machine → same key; different machine → different key.

function buildFingerprint() {
  const cpuModel = (os.cpus()[0] || {}).model || 'unknown-cpu';
  const parts = [
    os.hostname(),
    os.platform(),
    os.arch(),
    cpuModel,
  ];
  return crypto.createHash('sha256').update(parts.join('\x00')).digest('hex');
}

const FINGERPRINT = buildFingerprint();

// PBKDF2 derives a cryptographically strong 256-bit key from the fingerprint.
// The salt is application-specific but not secret; security comes from the
// fingerprint being machine-bound.
const MASTER_KEY = crypto.pbkdf2Sync(
  FINGERPRINT,
  'macro-recorder-v1-salt',
  100_000,
  32,
  'sha256'
);

// ── AES-256-GCM helpers ───────────────────────────────────────────────────────

/**
 * Encrypt an arbitrary JS object.
 * Returns { iv, tag, data } — all base64 strings.
 */
function encrypt(obj) {
  const iv      = crypto.randomBytes(12); // 96-bit nonce (GCM standard)
  const cipher  = crypto.createCipheriv('aes-256-gcm', MASTER_KEY, iv);
  const payload = Buffer.from(JSON.stringify(obj), 'utf8');
  const body    = Buffer.concat([cipher.update(payload), cipher.final()]);

  return {
    iv:   iv.toString('base64'),
    tag:  cipher.getAuthTag().toString('base64'),
    data: body.toString('base64'),
  };
}

/**
 * Decrypt an envelope produced by encrypt().
 * Throws if the auth tag doesn't verify (tamper detection).
 */
function decrypt(envelope) {
  const decipher = crypto.createDecipheriv(
    'aes-256-gcm',
    MASTER_KEY,
    Buffer.from(envelope.iv, 'base64')
  );
  decipher.setAuthTag(Buffer.from(envelope.tag, 'base64'));

  const plain = Buffer.concat([
    decipher.update(Buffer.from(envelope.data, 'base64')),
    decipher.final(),
  ]);
  return JSON.parse(plain.toString('utf8'));
}

// ── Filesystem storage ────────────────────────────────────────────────────────

const STORE_DIR = path.join(__dirname, 'macros');
fs.mkdirSync(STORE_DIR, { recursive: true });

const NAME_RE = /^[a-zA-Z0-9_-]{1,64}$/;

function assertValidName(name) {
  if (!NAME_RE.test(name)) {
    const err = new Error(`Invalid macro name "${name}" — use letters, digits, _ or -`);
    err.status = 400;
    throw err;
  }
}

function macroPath(name) {
  assertValidName(name);
  return path.join(STORE_DIR, `${name}.json`);
}

function saveMacro(name, events, meta = {}) {
  const envelope = encrypt({ events, meta });
  const record = {
    name,
    fingerprint: FINGERPRINT.slice(0, 16) + '…', // partial — not needed for decryption
    eventCount:  events.length,
    duration:    meta.duration || 0,
    createdAt:   meta.createdAt || new Date().toISOString(),
    ...envelope,
  };
  fs.writeFileSync(macroPath(name), JSON.stringify(record, null, 2), 'utf8');
}

function loadMacro(name) {
  const raw = JSON.parse(fs.readFileSync(macroPath(name), 'utf8'));
  return decrypt({ iv: raw.iv, tag: raw.tag, data: raw.data });
}

function listMacros() {
  return fs.readdirSync(STORE_DIR)
    .filter(f => f.endsWith('.json'))
    .map(f => {
      try {
        const r = JSON.parse(fs.readFileSync(path.join(STORE_DIR, f), 'utf8'));
        return {
          name:        r.name,
          eventCount:  r.eventCount,
          duration:    r.duration,
          createdAt:   r.createdAt,
          fingerprint: r.fingerprint,
        };
      } catch {
        return null;
      }
    })
    .filter(Boolean)
    .sort((a, b) => a.createdAt.localeCompare(b.createdAt));
}

function deleteMacro(name) {
  fs.unlinkSync(macroPath(name));
}

// ── Express app ───────────────────────────────────────────────────────────────

const app = express();
app.use(express.json({ limit: '10mb' }));

// Token auth — checked on every request
const TOKEN = process.env.MACRO_TOKEN || 'changeme';

app.use((req, res, next) => {
  const tok = req.headers['x-macro-token'] || req.query.token;
  if (tok !== TOKEN) {
    return res.status(401).json({ error: 'Unauthorized: missing or invalid X-Macro-Token' });
  }
  next();
});

// ── Routes ────────────────────────────────────────────────────────────────────

// GET /macros — list metadata for all stored macros
app.get('/macros', (_req, res) => {
  try {
    res.json(listMacros());
  } catch (e) {
    res.status(500).json({ error: e.message });
  }
});

// GET /macros/:name — decrypt and return events for playback
app.get('/macros/:name', (req, res) => {
  try {
    const payload = loadMacro(req.params.name);
    res.json(payload);
  } catch (e) {
    const status = e.code === 'ENOENT' ? 404 : e.status || 500;
    res.status(status).json({ error: e.message });
  }
});

// POST /macros — store a new macro
// Body: { name: string, events: Event[], meta?: object }
app.post('/macros', (req, res) => {
  const { name, events, meta } = req.body || {};

  if (typeof name !== 'string' || !Array.isArray(events)) {
    return res.status(400).json({ error: '"name" (string) and "events" (array) are required' });
  }

  try {
    saveMacro(name, events, {
      ...(meta || {}),
      createdAt: new Date().toISOString(),
      duration: events.reduce((s, e) => s + (e.delay || 0), 0),
    });
    res.status(201).json({ ok: true, name, eventCount: events.length });
  } catch (e) {
    res.status(e.status || 500).json({ error: e.message });
  }
});

// DELETE /macros/:name — remove a stored macro
app.delete('/macros/:name', (req, res) => {
  try {
    deleteMacro(req.params.name);
    res.json({ ok: true, name: req.params.name });
  } catch (e) {
    const status = e.code === 'ENOENT' ? 404 : e.status || 500;
    res.status(status).json({ error: e.message });
  }
});

// ── Startup ───────────────────────────────────────────────────────────────────

const PORT = parseInt(process.env.MACRO_PORT || '3000', 10);
const HOST = process.env.MACRO_HOST || '127.0.0.1';

app.listen(PORT, HOST, () => {
  const warn = TOKEN === 'changeme' ? ' ⚠  set MACRO_TOKEN env var' : ' [configured]';
  console.log(`Macro server    http://${HOST}:${PORT}`);
  console.log(`Fingerprint     ${FINGERPRINT.slice(0, 24)}…`);
  console.log(`Token           ${TOKEN === 'changeme' ? '[default — change in production]' : '[set]'}`);
  console.log(`Storage         ${STORE_DIR}`);
  console.log(`Encryption      AES-256-GCM / PBKDF2(fingerprint, 100k)`);
});
