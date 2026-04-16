#!/usr/bin/env node
'use strict';

/**
 * macro-client.js — CLI for recording and playing back macros
 *
 * Commands:
 *   node macro-client.js record <name>   Capture keystrokes & mouse clicks, send to server
 *   node macro-client.js play   <name>   Retrieve & replay a saved macro
 *   node macro-client.js list            Show all stored macros
 *   node macro-client.js delete <name>   Remove a macro from the server
 *
 * Environment variables:
 *   MACRO_SERVER  Base URL of macro-server.js  (default: http://127.0.0.1:3000)
 *   MACRO_TOKEN   Auth token                   (default: changeme)
 *
 * Recording:
 *   Puts the terminal in raw mode and enables SGR-extended mouse reporting.
 *   Every keystroke and mouse click is timestamped and buffered with an
 *   inter-event delay.  Press Ctrl+C to finish recording and upload.
 *
 * Playback:
 *   Downloads and decrypts the macro, then replays events with their original
 *   timing.  If xdotool is present (Linux/X11) it drives the system-level
 *   keyboard and mouse; otherwise events are echoed to the current terminal.
 */

const http         = require('http');
const https        = require('https');
const { execSync } = require('child_process');
const os           = require('os');

// ── Config ────────────────────────────────────────────────────────────────────

const SERVER = (process.env.MACRO_SERVER || 'http://127.0.0.1:3000').replace(/\/$/, '');
const TOKEN  = process.env.MACRO_TOKEN  || 'changeme';

// ── xdotool availability ──────────────────────────────────────────────────────

let HAS_XDOTOOL = false;
try {
  execSync('which xdotool', { stdio: 'ignore' });
  HAS_XDOTOOL = true;
} catch { /* not available */ }

// ── HTTP helper ───────────────────────────────────────────────────────────────

/**
 * Minimal promise-based HTTP client (no extra dependencies).
 * @param {string} method  GET | POST | DELETE
 * @param {string} route   e.g. '/macros/foo'
 * @param {object} [body]  JSON-serialisable body for POST
 * @returns {{ status: number, body: any }}
 */
function request(method, route, body) {
  return new Promise((resolve, reject) => {
    const url     = new URL(SERVER + route);
    const payload = body != null ? Buffer.from(JSON.stringify(body), 'utf8') : null;

    const options = {
      hostname: url.hostname,
      port:     url.port || (url.protocol === 'https:' ? 443 : 80),
      path:     url.pathname + url.search,
      method,
      headers: {
        'x-macro-token': TOKEN,
        ...(payload
          ? { 'Content-Type': 'application/json', 'Content-Length': payload.length }
          : {}),
      },
    };

    const transport = url.protocol === 'https:' ? https : http;
    const req = transport.request(options, (res) => {
      const chunks = [];
      res.on('data', c => chunks.push(c));
      res.on('end', () => {
        const raw = Buffer.concat(chunks).toString('utf8');
        let parsed;
        try { parsed = JSON.parse(raw); } catch { parsed = raw; }
        resolve({ status: res.statusCode, body: parsed });
      });
    });

    req.on('error', reject);
    if (payload) req.write(payload);
    req.end();
  });
}

// ── Mouse event parser (SGR extended protocol, \x1b[<…) ──────────────────────
// Enabled by ANSI escape: ESC [ ? 1003 h  (all motion)
//                          ESC [ ? 1006 h  (SGR extended coordinates)
//
// Packet format: ESC [ < Pb ; Px ; Py M   (press)
//                ESC [ < Pb ; Px ; Py m   (release)
// Pb encodes: button (bits 0-1), shift (2), meta (3), ctrl (4),
//             motion (5), scroll (6)

function parseMouse(buf) {
  const s = buf.toString('binary');
  const m = s.match(/^\x1b\[<(\d+);(\d+);(\d+)([Mm])/);
  if (!m) return null;

  const pb = parseInt(m[1], 10);
  return {
    type:    'mouse',
    x:       parseInt(m[2], 10),
    y:       parseInt(m[3], 10),
    button:  pb & 3,
    shift:   !!(pb &  4),
    meta:    !!(pb &  8),
    ctrl:    !!(pb & 16),
    motion:  !!(pb & 32),
    scroll:  !!(pb & 64),
    release: m[4] === 'm',
  };
}

// ── Key → xdotool key-name mapping ───────────────────────────────────────────

const XDOTOOL_KEY_MAP = {
  '\r':   'Return',
  '\n':   'Return',
  '\t':   'Tab',
  '\x7f': 'BackSpace',
  '\x08': 'BackSpace',
  '\x1b': 'Escape',
  ' ':    'space',
};

// xdotool modifier prefixes built from a recorded modifier mask
function xdotoolModifiers(ev) {
  const mods = [];
  if (ev.ctrl)  mods.push('ctrl');
  if (ev.shift) mods.push('shift');
  if (ev.meta)  mods.push('alt');
  return mods;
}

// ── record <name> ─────────────────────────────────────────────────────────────

async function cmdRecord(name) {
  if (!name) die('Usage: record <name>');

  if (!process.stdin.isTTY) {
    die('record requires an interactive TTY (stdin must be a terminal)');
  }

  console.log(`\nRecording macro "${name}"`);
  console.log('  • Keystrokes and mouse clicks are captured');
  console.log('  • Motion events are skipped (too noisy)');
  console.log('  • Press Ctrl+C to finish and upload\n');

  const events   = [];
  let   lastTime = Date.now();
  let   done     = false;

  // Enter raw mode; enable SGR mouse reporting (all-motion + extended coords)
  process.stdin.setRawMode(true);
  process.stdin.resume();
  process.stdout.write('\x1b[?1003h\x1b[?1006h');

  const cleanup = () => {
    process.stdout.write('\x1b[?1003l\x1b[?1006l'); // disable mouse reporting
    try { process.stdin.setRawMode(false); } catch {}
    process.stdin.pause();
  };

  await new Promise((resolve) => {
    process.stdin.on('data', (buf) => {
      if (done) return;

      const now   = Date.now();
      const delay = now - lastTime;
      lastTime    = now;

      const mouse = parseMouse(buf);

      if (mouse) {
        // Skip pure-motion events to keep recordings compact
        if (mouse.motion) return;

        events.push({ ...mouse, delay });
        process.stdout.write(
          `  mouse  btn=${mouse.button} x=${mouse.x} y=${mouse.y}` +
          `${mouse.release ? ' [release]' : ''}\n`
        );
        return;
      }

      const str = buf.toString('utf8');

      // Ctrl+C — stop recording
      if (str === '\x03') {
        done = true;
        cleanup();
        resolve();
        return;
      }

      // Ordinary key (including escape sequences for arrows, F-keys, etc.)
      events.push({ type: 'key', key: str, raw: buf.toString('base64'), delay });

      // Human-readable display
      const label = str
        .replace(/\x1b/g, '<ESC>')
        .replace(/\r/g,   '<CR>')
        .replace(/\n/g,   '<LF>')
        .replace(/\t/g,   '<TAB>');
      process.stdout.write(`  key    ${JSON.stringify(label)}\n`);
    });
  });

  const duration = events.reduce((s, e) => s + (e.delay || 0), 0);
  console.log(`\nRecorded ${events.length} event(s) over ${(duration / 1000).toFixed(1)}s`);
  console.log('Uploading…');

  const res = await request('POST', '/macros', {
    name,
    events,
    meta: {
      os:       os.platform(),
      arch:     os.arch(),
      hostname: os.hostname(),
      duration,
    },
  });

  if (res.status === 201) {
    console.log(`Saved  "${name}"  (${events.length} events, ${(duration / 1000).toFixed(1)}s)`);
  } else {
    die(`Server error ${res.status}: ${JSON.stringify(res.body)}`);
  }
}

// ── play <name> ───────────────────────────────────────────────────────────────

async function cmdPlay(name) {
  if (!name) die('Usage: play <name>');

  const res = await request('GET', `/macros/${encodeURIComponent(name)}`);
  if (res.status !== 200) {
    die(`Cannot load macro: ${JSON.stringify(res.body)}`);
  }

  const { events = [], meta = {} } = res.body;
  const total = events.reduce((s, e) => s + (e.delay || 0), 0);

  console.log(`\nPlaying  "${name}"  — ${events.length} event(s), ~${(total / 1000).toFixed(1)}s`);
  if (meta.os) console.log(`Recorded on  ${meta.os} / ${meta.hostname || 'unknown'}`);

  if (!HAS_XDOTOOL) {
    console.log('xdotool not found — echoing key events to terminal; mouse coords logged only\n');
  } else {
    console.log('Using xdotool for system-level input injection\n');
  }

  for (const ev of events) {
    if (ev.delay > 0) await sleep(ev.delay);

    if (ev.type === 'key') {
      await replayKey(ev);
    } else if (ev.type === 'mouse') {
      await replayMouse(ev);
    }
  }

  console.log('\nPlayback complete.');
}

async function replayKey(ev) {
  const key = ev.key;

  if (HAS_XDOTOOL) {
    const mapped = XDOTOOL_KEY_MAP[key];
    try {
      if (mapped) {
        execSync(`xdotool key -- ${mapped}`, { stdio: 'ignore' });
      } else if (key.length === 1 && key.charCodeAt(0) >= 32 && key.charCodeAt(0) < 127) {
        // Printable ASCII — use xdotool type
        execSync(`xdotool type --clearmodifiers --delay 0 -- ${shellQuote(key)}`, { stdio: 'ignore' });
      } else {
        // Escape sequences (arrows, F-keys, etc.) — fall through to stdout
        process.stdout.write(key);
      }
    } catch {
      process.stdout.write(key);
    }
  } else {
    process.stdout.write(key);
  }
}

async function replayMouse(ev) {
  if (ev.motion) return; // shouldn't be stored, but guard anyway

  if (HAS_XDOTOOL) {
    try {
      execSync(`xdotool mousemove -- ${ev.x} ${ev.y}`, { stdio: 'ignore' });
      if (!ev.release) {
        const btn = (ev.button || 0) + 1; // xdotool uses 1-indexed buttons
        execSync(`xdotool click -- ${btn}`, { stdio: 'ignore' });
      }
    } catch { /* non-fatal */ }
  } else {
    process.stdout.write(
      `[mouse btn=${ev.button} x=${ev.x} y=${ev.y}${ev.release ? ' release' : ''}]\n`
    );
  }
}

// ── list ──────────────────────────────────────────────────────────────────────

async function cmdList() {
  const res = await request('GET', '/macros');
  if (res.status !== 200) die(`Server error: ${JSON.stringify(res.body)}`);

  const macros = res.body;
  if (!macros.length) {
    console.log('No macros stored.');
    return;
  }

  const col = (s, w) => String(s || '').padEnd(w);

  console.log(`\n${'NAME'.padEnd(24)} ${'EVENTS'.padStart(6)} ${'DURATION'.padStart(9)}  CREATED`);
  console.log('─'.repeat(72));
  for (const m of macros) {
    const dur = m.duration ? `${(m.duration / 1000).toFixed(1)}s` : '—';
    console.log(
      `${col(m.name, 24)} ${String(m.eventCount || 0).padStart(6)} ${dur.padStart(9)}  ${m.createdAt}`
    );
  }
  console.log();
}

// ── delete <name> ─────────────────────────────────────────────────────────────

async function cmdDelete(name) {
  if (!name) die('Usage: delete <name>');

  const res = await request('DELETE', `/macros/${encodeURIComponent(name)}`);
  if (res.status === 200) {
    console.log(`Deleted macro "${name}"`);
  } else {
    die(`Error ${res.status}: ${JSON.stringify(res.body)}`);
  }
}

// ── Utilities ─────────────────────────────────────────────────────────────────

function sleep(ms) {
  return new Promise(r => setTimeout(r, ms));
}

// Single-quote a string for safe shell interpolation
function shellQuote(s) {
  return "'" + String(s).replace(/'/g, "'\\''") + "'";
}

function die(msg) {
  console.error(msg);
  process.exit(1);
}

// ── Help text ─────────────────────────────────────────────────────────────────

const USAGE = `
Usage:
  node macro-client.js record <name>   Record keystrokes & mouse clicks
  node macro-client.js play   <name>   Play back a saved macro
  node macro-client.js list            List all stored macros
  node macro-client.js delete <name>   Delete a macro from the server

Environment:
  MACRO_SERVER   Server base URL  (default: http://127.0.0.1:3000)
  MACRO_TOKEN    Auth token       (default: changeme)

Playback notes:
  • If xdotool is installed (Linux/X11) keyboard and mouse events are injected
    system-wide so they reach any focused window.
  • Without xdotool, key events are written to stdout and mouse coordinates
    are logged; useful for terminal-only workflows.
`.trim();

// ── Entry point ───────────────────────────────────────────────────────────────

const [,, cmd, arg] = process.argv;

(async () => {
  switch (cmd) {
    case 'record': await cmdRecord(arg); break;
    case 'play':   await cmdPlay(arg);   break;
    case 'list':   await cmdList();      break;
    case 'delete': await cmdDelete(arg); break;
    default:
      console.log(USAGE);
      process.exit(cmd ? 1 : 0);
  }
})().catch(e => die(e.message));
