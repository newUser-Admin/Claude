/**
 * interaction-engine.js
 *
 * High-Fidelity Interaction Engine
 *
 * Integrates four subsystems into a single cohesive ES module:
 *
 *   §1  BinarySerializer   — compact binary encoding/decoding of structured data
 *   §2  JitterBuffer       — adaptive playout buffer for jittery/out-of-order streams
 *   §3  NetworkSync        — delta-compressed state synchronization with ACK tracking
 *   §4  MemoryPersistence  — history-less snapshot persistence (memory or localStorage)
 *   §5  InteractionEngine  — facade that composes all four subsystems
 *
 * Usage:
 *   import { InteractionEngine } from './interaction-engine.js';
 *
 *   const engine = new InteractionEngine({ persistence: 'local', storageKey: 'myApp' });
 *   engine.connect((frame) => websocket.send(frame));
 *   engine.on('update', (state) => render(state));
 *
 *   engine.setState({ score: 42, player: 'Alice' });
 *   engine.flush();     // transmit pending delta
 *   engine.persist();   // snapshot state to storage
 *
 *   // On incoming data:
 *   websocket.onmessage = (e) => engine.receive(e.data);
 */

"use strict";

/* ═══════════════════════════════════════════════════════════════════════════
   §1  BINARY SERIALIZER
   ═══════════════════════════════════════════════════════════════════════════
   Encodes JavaScript values to compact binary ArrayBuffers and back.

   Type tags (1 byte):
     0x00  null / undefined
     0x01  boolean false
     0x02  boolean true
     0x03  uint8    (0–255)
     0x04  uint16   (0–65535)
     0x05  uint32   (0–4294967295)
     0x06  float64  (any IEEE-754 double)
     0x07  int8     (–128–127)
     0x08  int16    (–32768–32767)
     0x09  int32    (–2147483648–2147483647)
     0x0A  string   [uint32 byteLen][UTF-8 bytes]
     0x0B  binary   [uint32 byteLen][raw bytes]  (Uint8Array / ArrayBuffer)
     0x0C  array    [uint32 itemCount][items…]
     0x0D  object   [uint32 pairCount][key(string) value …]
*/

const TAG = Object.freeze({
  NULL:    0x00,
  FALSE:   0x01,
  TRUE:    0x02,
  UINT8:   0x03,
  UINT16:  0x04,
  UINT32:  0x05,
  FLOAT64: 0x06,
  INT8:    0x07,
  INT16:   0x08,
  INT32:   0x09,
  STRING:  0x0A,
  BINARY:  0x0B,
  ARRAY:   0x0C,
  OBJECT:  0x0D,
});

// ── BufferWriter ─────────────────────────────────────────────────────────────
// Grows automatically as data is appended.
class BufferWriter {
  constructor(initialCapacity = 256) {
    this._buf  = new Uint8Array(initialCapacity);
    this._view = new DataView(this._buf.buffer);
    this._pos  = 0;
  }

  _ensure(bytes) {
    if (this._pos + bytes <= this._buf.length) return;
    let cap = this._buf.length;
    while (cap < this._pos + bytes) cap *= 2;
    const next = new Uint8Array(cap);
    next.set(this._buf);
    this._buf  = next;
    this._view = new DataView(next.buffer);
  }

  writeUint8(v)   { this._ensure(1); this._view.setUint8(this._pos, v);              this._pos += 1; }
  writeUint16(v)  { this._ensure(2); this._view.setUint16(this._pos, v, false);       this._pos += 2; }
  writeUint32(v)  { this._ensure(4); this._view.setUint32(this._pos, v, false);       this._pos += 4; }
  writeInt8(v)    { this._ensure(1); this._view.setInt8(this._pos, v);               this._pos += 1; }
  writeInt16(v)   { this._ensure(2); this._view.setInt16(this._pos, v, false);        this._pos += 2; }
  writeInt32(v)   { this._ensure(4); this._view.setInt32(this._pos, v, false);        this._pos += 4; }
  writeFloat64(v) { this._ensure(8); this._view.setFloat64(this._pos, v, false);      this._pos += 8; }

  writeBytes(u8) {
    this._ensure(u8.length);
    this._buf.set(u8, this._pos);
    this._pos += u8.length;
  }

  toUint8Array() { return this._buf.subarray(0, this._pos); }
}

// ── BufferReader ─────────────────────────────────────────────────────────────
class BufferReader {
  constructor(buffer) {
    const u8 = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
    this._buf  = u8;
    this._view = new DataView(u8.buffer, u8.byteOffset, u8.byteLength);
    this._pos  = 0;
  }

  get remaining() { return this._buf.length - this._pos; }

  _need(n) {
    if (this._pos + n > this._buf.length)
      throw new RangeError(
        `BufferReader underflow: need ${n} byte(s), ${this.remaining} remaining`
      );
  }

  readUint8()   { this._need(1); return this._view.getUint8(this._pos++); }
  readUint16()  { this._need(2); const v = this._view.getUint16(this._pos, false);  this._pos += 2; return v; }
  readUint32()  { this._need(4); const v = this._view.getUint32(this._pos, false);  this._pos += 4; return v; }
  readInt8()    { this._need(1); return this._view.getInt8(this._pos++); }
  readInt16()   { this._need(2); const v = this._view.getInt16(this._pos, false);   this._pos += 2; return v; }
  readInt32()   { this._need(4); const v = this._view.getInt32(this._pos, false);   this._pos += 4; return v; }
  readFloat64() { this._need(8); const v = this._view.getFloat64(this._pos, false); this._pos += 8; return v; }

  readBytes(n) {
    this._need(n);
    const slice = this._buf.slice(this._pos, this._pos + n);
    this._pos += n;
    return slice;
  }
}

// UTF-8 codec (reused across all encode/decode calls)
const _te = new TextEncoder();
const _td = new TextDecoder();

export class BinarySerializer {
  /**
   * Encode a JavaScript value to a Uint8Array.
   * Supported types: null, undefined, boolean, number, string,
   *                  Uint8Array, ArrayBuffer, Array, plain Object.
   * @param   {*} value
   * @returns {Uint8Array}
   */
  static encode(value) {
    const w = new BufferWriter();
    BinarySerializer._write(w, value);
    return w.toUint8Array();
  }

  /**
   * Decode a Uint8Array / ArrayBuffer back to a JavaScript value.
   * @param   {Uint8Array|ArrayBuffer} buffer
   * @returns {*}
   */
  static decode(buffer) {
    const r = new BufferReader(buffer);
    return BinarySerializer._read(r);
  }

  // ── Private encode helpers ───────────────────────────────────────────────

  static _write(w, value) {
    if (value === null || value === undefined) {
      w.writeUint8(TAG.NULL); return;
    }
    if (typeof value === 'boolean') {
      w.writeUint8(value ? TAG.TRUE : TAG.FALSE); return;
    }
    if (typeof value === 'number') {
      BinarySerializer._writeNumber(w, value); return;
    }
    if (typeof value === 'string') {
      const bytes = _te.encode(value);
      w.writeUint8(TAG.STRING);
      w.writeUint32(bytes.length);
      w.writeBytes(bytes);
      return;
    }
    if (value instanceof Uint8Array || value instanceof ArrayBuffer) {
      const u8 = value instanceof ArrayBuffer ? new Uint8Array(value) : value;
      w.writeUint8(TAG.BINARY);
      w.writeUint32(u8.length);
      w.writeBytes(u8);
      return;
    }
    if (Array.isArray(value)) {
      w.writeUint8(TAG.ARRAY);
      w.writeUint32(value.length);
      for (const item of value) BinarySerializer._write(w, item);
      return;
    }
    if (typeof value === 'object') {
      const keys = Object.keys(value);
      w.writeUint8(TAG.OBJECT);
      w.writeUint32(keys.length);
      for (const key of keys) {
        BinarySerializer._write(w, key);        // key always a string
        BinarySerializer._write(w, value[key]);
      }
      return;
    }
    // Unknown type — encode as null
    w.writeUint8(TAG.NULL);
  }

  static _writeNumber(w, n) {
    if (Number.isInteger(n)) {
      if (n >= 0) {
        if (n <= 0xFF)         { w.writeUint8(TAG.UINT8);   w.writeUint8(n);   return; }
        if (n <= 0xFFFF)       { w.writeUint8(TAG.UINT16);  w.writeUint16(n);  return; }
        if (n <= 0xFFFFFFFF)   { w.writeUint8(TAG.UINT32);  w.writeUint32(n);  return; }
      } else {
        if (n >= -128)         { w.writeUint8(TAG.INT8);    w.writeInt8(n);    return; }
        if (n >= -32768)       { w.writeUint8(TAG.INT16);   w.writeInt16(n);   return; }
        if (n >= -2147483648)  { w.writeUint8(TAG.INT32);   w.writeInt32(n);   return; }
      }
    }
    w.writeUint8(TAG.FLOAT64);
    w.writeFloat64(n);
  }

  // ── Private decode helpers ───────────────────────────────────────────────

  static _read(r) {
    const tag = r.readUint8();
    switch (tag) {
      case TAG.NULL:    return null;
      case TAG.FALSE:   return false;
      case TAG.TRUE:    return true;
      case TAG.UINT8:   return r.readUint8();
      case TAG.UINT16:  return r.readUint16();
      case TAG.UINT32:  return r.readUint32();
      case TAG.INT8:    return r.readInt8();
      case TAG.INT16:   return r.readInt16();
      case TAG.INT32:   return r.readInt32();
      case TAG.FLOAT64: return r.readFloat64();
      case TAG.STRING: {
        const len = r.readUint32();
        return _td.decode(r.readBytes(len));
      }
      case TAG.BINARY: {
        const len = r.readUint32();
        return r.readBytes(len);
      }
      case TAG.ARRAY: {
        const count = r.readUint32();
        const arr = new Array(count);
        for (let i = 0; i < count; i++) arr[i] = BinarySerializer._read(r);
        return arr;
      }
      case TAG.OBJECT: {
        const count = r.readUint32();
        const obj   = Object.create(null);
        for (let i = 0; i < count; i++) {
          const key  = BinarySerializer._read(r);
          obj[key]   = BinarySerializer._read(r);
        }
        return obj;
      }
      default:
        throw new RangeError(
          `BinarySerializer: unknown type tag 0x${tag.toString(16).padStart(2, '0')}`
        );
    }
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
   §2  JITTER BUFFER
   ═══════════════════════════════════════════════════════════════════════════
   Adaptive playout buffer for real-time packet streams.

   Each incoming packet must carry:
     { seq: <uint>, ts: <sender clock ms>, data: <any> }

   The buffer:
     • Holds packets until their playout deadline arrives
     • Re-orders out-of-sequence arrivals using a sorted queue
     • Drops duplicates and packets that arrived after their playout time
     • Adapts the target delay with an EWMA of measured inter-arrival jitter
       (RFC 3550 §A.8 formula: J += (|D| − J) / 16)
*/
export class JitterBuffer {
  /**
   * @param {object} [opts]
   * @param {number} [opts.targetDelay=40]  Initial playout delay in ms
   * @param {number} [opts.minDelay=0]      Minimum allowed playout delay in ms
   * @param {number} [opts.maxDelay=300]    Maximum allowed playout delay in ms
   * @param {number} [opts.capacity=256]    Maximum packets held at once
   */
  constructor({ targetDelay = 40, minDelay = 0, maxDelay = 300, capacity = 256 } = {}) {
    this._targetDelay = targetDelay;
    this._minDelay    = minDelay;
    this._maxDelay    = maxDelay;
    this._capacity    = capacity;

    this._queue        = [];    // packets sorted ascending by seq
    this._nextSeq      = null;  // next sequence number expected for playout

    // Jitter state
    this._jitter       = 0;     // EWMA jitter estimate (ms)
    this._lastArrival  = null;  // performance.now() at last push()
    this._lastSenderTs = null;  // packet.ts at last push()

    // Cumulative statistics
    this._stats = { received: 0, emitted: 0, dropped: 0, reordered: 0 };
  }

  // ── Public API ───────────────────────────────────────────────────────────

  /**
   * Enqueue an incoming packet.
   * @param {{ seq: number, ts: number, data: * }} packet
   */
  push(packet) {
    const now = performance.now();
    this._stats.received++;

    // Update jitter estimate from inter-arrival timing
    if (this._lastArrival !== null) {
      const arrivalDiff = now - this._lastArrival;
      const senderDiff  = packet.ts - this._lastSenderTs;
      const d = Math.abs(arrivalDiff - senderDiff);
      this._jitter += (d - this._jitter) * 0.0625; // EWMA α = 1/16
      this._adaptTargetDelay();
    }
    this._lastArrival  = now;
    this._lastSenderTs = packet.ts;

    // Anchor next-seq on first packet received
    if (this._nextSeq === null) this._nextSeq = packet.seq;

    // Discard packets that have already been emitted (too late)
    if (packet.seq < this._nextSeq) {
      this._stats.dropped++;
      return;
    }

    // Overflow guard: evict the oldest when at capacity
    if (this._queue.length >= this._capacity) {
      this._queue.shift();
      this._stats.dropped++;
    }

    // Sorted insert by seq
    const idx = this._insertIndex(packet.seq);
    if (idx < this._queue.length && this._queue[idx].seq === packet.seq) {
      // Duplicate — discard silently
      return;
    }
    this._queue.splice(idx, 0, { ...packet, _arrived: now });

    if (packet.seq !== this._nextSeq) this._stats.reordered++;
  }

  /**
   * Poll the next packet if it is due for playout.
   * Returns null if the head packet is missing or not yet ready.
   * Call on every frame / update tick.
   * @returns {{ seq: number, ts: number, data: * } | null}
   */
  poll() {
    if (this._queue.length === 0) return null;
    const head = this._queue[0];
    if (head.seq !== this._nextSeq) return null;                         // sequence gap
    if (performance.now() - head._arrived < this._targetDelay) return null; // not yet due

    this._queue.shift();
    this._nextSeq++;
    this._stats.emitted++;
    return head;
  }

  /**
   * Drain all sequentially-ready packets, ignoring playout timing.
   * Useful for non-real-time / batch processing modes.
   * @returns {Array<{ seq: number, ts: number, data: * }>}
   */
  drainReady() {
    const out = [];
    while (this._queue.length > 0 && this._queue[0].seq === this._nextSeq) {
      const pkt = this._queue.shift();
      this._nextSeq++;
      this._stats.emitted++;
      out.push(pkt);
    }
    return out;
  }

  /**
   * Current EWMA jitter estimate (ms).
   * @type {number}
   */
  get jitter() { return this._jitter; }

  /**
   * Current adaptive playout delay (ms).
   * @type {number}
   */
  get targetDelay() { return this._targetDelay; }

  /**
   * Number of packets currently buffered.
   * @type {number}
   */
  get depth() { return this._queue.length; }

  /**
   * Cumulative statistics snapshot.
   * @type {{ received: number, emitted: number, dropped: number, reordered: number }}
   */
  get stats() { return { ...this._stats }; }

  /** Clear all buffered packets and reset sequence tracking. */
  reset() {
    this._queue        = [];
    this._nextSeq      = null;
    this._jitter       = 0;
    this._lastArrival  = null;
    this._lastSenderTs = null;
  }

  // ── Private helpers ──────────────────────────────────────────────────────

  _adaptTargetDelay() {
    // Desired delay = minDelay + 2× jitter (2σ safety headroom)
    const desired = this._minDelay + 2 * this._jitter;
    if (desired > this._targetDelay) {
      // Increase aggressively to avoid underrun
      this._targetDelay = Math.min(desired, this._maxDelay);
    } else {
      // Decrease slowly to avoid oscillation (5% per update)
      this._targetDelay += (desired - this._targetDelay) * 0.05;
      this._targetDelay  = Math.max(this._targetDelay, this._minDelay);
    }
  }

  // Binary search for sorted-insert position
  _insertIndex(seq) {
    let lo = 0, hi = this._queue.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (this._queue[mid].seq < seq) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
   §3  NETWORK SYNC
   ═══════════════════════════════════════════════════════════════════════════
   Delta-compressed, sequence-numbered bidirectional state synchronization.

   Maintains:
     • _localState   — the application's current intended state
     • _baseState    — the last remote-acknowledged snapshot (for delta diffing)
     • _remoteState  — the most recently received remote state

   On flush(), only keys that changed since the last ACK are transmitted.
   On receive(), incoming deltas are merged and an ACK is sent back.

   Binary frame envelope (BinarySerializer object):
     { type: 'update'|'ack',
       seq:  uint32,
       ack:  uint32,    (last remote seq we have seen)
       ts:   float64,   (sender wall-clock ms)
       delta?:     object,    (key → newValue, update frames only)
       deletions?: string[] } (update frames only)
*/
export class NetworkSync {
  /**
   * @param {object} [opts]
   * @param {function(Uint8Array): void} [opts.onSend]      Called with every outbound binary frame
   * @param {function(object): void}     [opts.onUpdate]    Called when remote state changes
   * @param {number}                     [opts.maxUnacked=64] Max unacked outbound deltas to keep
   */
  constructor({ onSend = null, onUpdate = null, maxUnacked = 64 } = {}) {
    this._onSend     = onSend;
    this._onUpdate   = onUpdate;
    this._maxUnacked = maxUnacked;

    this._localState  = Object.create(null);
    this._baseState   = Object.create(null); // remote's last ACKed view of our state
    this._remoteState = Object.create(null); // latest known remote state

    this._localSeq  = 0; // monotonically increasing outbound seq
    this._remoteSeq = 0; // last inbound seq processed

    // Map<seq → { delta, deletions, ts }> — for retransmit on reconnect
    this._unacked = new Map();
  }

  // ── Local state management ───────────────────────────────────────────────

  /**
   * Set one key or merge an object into local state.
   * @param {string|object} keyOrObj
   * @param {*}             [value]
   */
  set(keyOrObj, value) {
    if (typeof keyOrObj === 'string') {
      this._localState[keyOrObj] = value;
    } else {
      Object.assign(this._localState, keyOrObj);
    }
  }

  /**
   * Remove a key from local state.
   * @param {string} key
   */
  delete(key) {
    delete this._localState[key];
  }

  /**
   * Return a shallow copy of the merged state (remote ← local overlay).
   * @returns {object}
   */
  getState() {
    return Object.assign(Object.create(null), this._remoteState, this._localState);
  }

  // ── Outbound ─────────────────────────────────────────────────────────────

  /**
   * Compute and transmit a delta frame if anything has changed.
   * No-ops (returns null) when there are no pending changes.
   * @returns {Uint8Array|null}
   */
  flush() {
    const { delta, deletions } = this._computeDelta();
    if (Object.keys(delta).length === 0 && deletions.length === 0) return null;

    const seq = ++this._localSeq;
    const frame = {
      type: 'update',
      seq,
      ack: this._remoteSeq,
      ts:  Date.now(),
      delta,
      deletions,
    };
    const encoded = BinarySerializer.encode(frame);

    // Retain for potential retransmit; evict oldest if window full
    if (this._unacked.size >= this._maxUnacked) {
      const oldest = this._unacked.keys().next().value;
      this._unacked.delete(oldest);
    }
    this._unacked.set(seq, { delta, deletions, ts: frame.ts });

    if (this._onSend) this._onSend(encoded);
    return encoded;
  }

  // ── Inbound ───────────────────────────────────────────────────────────────

  /**
   * Process a raw binary frame from the network.
   * @param {Uint8Array|ArrayBuffer} rawFrame
   */
  receive(rawFrame) {
    let frame;
    try {
      frame = BinarySerializer.decode(rawFrame);
    } catch (err) {
      console.warn('[NetworkSync] Failed to decode frame:', err);
      return;
    }

    if (frame.type === 'update') {
      this._handleUpdate(frame);
    } else if (frame.type === 'ack') {
      this._handleAck(frame);
    }
  }

  /**
   * Retransmit every unacknowledged delta (e.g. after reconnect).
   */
  retransmitAll() {
    for (const [seq, entry] of this._unacked) {
      const frame = BinarySerializer.encode({
        type: 'update', seq, ack: this._remoteSeq,
        ts: entry.ts, delta: entry.delta, deletions: entry.deletions,
      });
      if (this._onSend) this._onSend(frame);
    }
  }

  /** Reset all sync state (call on disconnection). */
  reset() {
    this._localSeq    = 0;
    this._remoteSeq   = 0;
    this._baseState   = Object.create(null);
    this._remoteState = Object.create(null);
    this._unacked.clear();
  }

  // ── Private helpers ──────────────────────────────────────────────────────

  _handleUpdate(frame) {
    // Reject replayed or out-of-order frames
    if (frame.seq <= this._remoteSeq) return;
    this._remoteSeq = frame.seq;

    if (frame.delta)     Object.assign(this._remoteState, frame.delta);
    if (frame.deletions) frame.deletions.forEach(k => delete this._remoteState[k]);

    // Send ACK
    const ack = BinarySerializer.encode({
      type: 'ack',
      seq:  this._localSeq,
      ack:  frame.seq,
      ts:   Date.now(),
    });
    if (this._onSend) this._onSend(ack);

    if (this._onUpdate) this._onUpdate(this.getState());
  }

  _handleAck(frame) {
    // Advance base state up to the acknowledged sequence
    for (const [seq, entry] of this._unacked) {
      if (seq <= frame.ack) {
        Object.assign(this._baseState, entry.delta);
        entry.deletions.forEach(k => delete this._baseState[k]);
        this._unacked.delete(seq);
      }
    }
  }

  _computeDelta() {
    const delta     = Object.create(null);
    const deletions = [];

    for (const key of Object.keys(this._localState)) {
      if (!Object.is(this._localState[key], this._baseState[key])) {
        delta[key] = this._localState[key];
      }
    }
    for (const key of Object.keys(this._baseState)) {
      if (!(key in this._localState)) deletions.push(key);
    }

    return { delta, deletions };
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
   §4  HISTORY-LESS MEMORY PERSISTENCE
   ═══════════════════════════════════════════════════════════════════════════
   Stores the latest snapshot of arbitrary state with zero history retained.
   Each write() atomically replaces any prior value for that key.

   Backends:
     'memory'  — in-process Map; fast, ephemeral (cleared on page reload)
     'local'   — window.localStorage; survives reloads, ~5 MB limit
*/
export class MemoryPersistence {
  /**
   * @param {object} [opts]
   * @param {'memory'|'local'} [opts.backend='memory']  Storage backend
   * @param {string}           [opts.namespace='ie']    Prefix for storage keys
   */
  constructor({ backend = 'memory', namespace = 'ie' } = {}) {
    this._backend   = backend;
    this._namespace = namespace;
    this._store     = new Map(); // used only for 'memory' backend
  }

  /**
   * Persist a value under key, replacing any prior value atomically.
   * @param {string} key
   * @param {*}      value  Must be BinarySerializer-encodable
   */
  write(key, value) {
    const encoded = BinarySerializer.encode(value);
    if (this._backend === 'local') {
      try {
        localStorage.setItem(this._nsKey(key), _u8ToBase64(encoded));
      } catch (err) {
        console.warn('[MemoryPersistence] localStorage write failed:', err);
      }
    } else {
      this._store.set(key, encoded.slice()); // copy to avoid aliasing
    }
  }

  /**
   * Retrieve the last persisted value for key, or undefined if absent.
   * @param   {string} key
   * @returns {*}
   */
  read(key) {
    let encoded;
    if (this._backend === 'local') {
      const raw = localStorage.getItem(this._nsKey(key));
      if (raw === null) return undefined;
      try { encoded = _base64ToU8(raw); } catch { return undefined; }
    } else {
      encoded = this._store.get(key);
      if (!encoded) return undefined;
    }
    try { return BinarySerializer.decode(encoded); } catch { return undefined; }
  }

  /**
   * Remove a persisted key.
   * @param {string} key
   */
  clear(key) {
    if (this._backend === 'local') {
      localStorage.removeItem(this._nsKey(key));
    } else {
      this._store.delete(key);
    }
  }

  /** Remove all keys in this namespace. */
  clearAll() {
    if (this._backend === 'local') {
      const prefix = this._nsKey('');
      for (const k of Object.keys(localStorage)) {
        if (k.startsWith(prefix)) localStorage.removeItem(k);
      }
    } else {
      this._store.clear();
    }
  }

  _nsKey(key) { return `${this._namespace}:${key}`; }
}

// ── Base64 codec (avoids btoa stack-overflow on large buffers) ───────────────
function _u8ToBase64(u8) {
  let bin = '';
  const chunk = 0x8000;
  for (let i = 0; i < u8.length; i += chunk) {
    bin += String.fromCharCode(...u8.subarray(i, i + chunk));
  }
  return btoa(bin);
}

function _base64ToU8(b64) {
  const bin = atob(b64);
  const u8  = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
  return u8;
}

/* ═══════════════════════════════════════════════════════════════════════════
   §5  INTERACTION ENGINE  (Facade)
   ═══════════════════════════════════════════════════════════════════════════
   Composes BinarySerializer, JitterBuffer, NetworkSync, and MemoryPersistence
   into a unified high-fidelity engine with a clean event-driven API.

   Data-flow diagram:

     Outbound
       app  ──setState()──▶  NetworkSync ──flush()──▶  BinarySerializer  ──▶  wire

     Inbound
       wire ──receive()──▶  JitterBuffer ──[tick]──▶  NetworkSync ──onUpdate()──▶  app

     Snapshot
       app  ──persist()──▶  MemoryPersistence
       app  ──restore()──▶  MemoryPersistence ──▶  NetworkSync.set()
*/
export class InteractionEngine {
  /**
   * @param {object} [opts]
   * @param {'memory'|'local'} [opts.persistence='memory']  Persistence backend
   * @param {string}           [opts.storageKey='engine']   Key used for state snapshots
   * @param {number}           [opts.tickRate=50]           Jitter-buffer poll interval (ms)
   * @param {object}           [opts.jitter={}]             Options forwarded to JitterBuffer
   */
  constructor({
    persistence = 'memory',
    storageKey  = 'engine',
    tickRate    = 50,
    jitter      = {},
  } = {}) {
    this._storageKey = storageKey;
    this._tickRate   = tickRate;
    this._tickHandle = null;
    this._sendFn     = null;

    this._jitterBuffer = new JitterBuffer(jitter);

    this._persistence  = new MemoryPersistence({
      backend:   persistence,
      namespace: storageKey,
    });

    this._sync = new NetworkSync({
      onSend:   (frame) => { if (this._sendFn) this._sendFn(frame); },
      onUpdate: (state) => this._emit('update', state),
    });

    // Event bus: Map<eventName, Set<listener>>
    this._listeners = new Map();
  }

  // ── Connection lifecycle ─────────────────────────────────────────────────

  /**
   * Connect the engine to a transport.
   * Starts the internal jitter-buffer poll loop.
   * @param {function(Uint8Array): void} sendFn  Called for every outbound binary frame
   */
  connect(sendFn) {
    this._sendFn = sendFn;
    this._startTick();
    this._emit('connect');
  }

  /**
   * Disconnect from the transport and stop the poll loop.
   * Resets in-flight sync state; persisted snapshots are unaffected.
   */
  disconnect() {
    this._stopTick();
    this._sendFn = null;
    this._sync.reset();
    this._jitterBuffer.reset();
    this._emit('disconnect');
  }

  // ── State management ──────────────────────────────────────────────────────

  /**
   * Update local state.
   * @param {string|object} keyOrObj  Key string or object of key→value pairs
   * @param {*}             [value]   Value (when keyOrObj is a string)
   */
  setState(keyOrObj, value) {
    if (typeof keyOrObj === 'string') {
      this._sync.set(keyOrObj, value);
    } else {
      this._sync.set(keyOrObj);
    }
  }

  /**
   * Return the current merged state (remote values overlaid with local).
   * @returns {object}
   */
  getState() {
    return this._sync.getState();
  }

  /**
   * Encode and transmit any pending state delta immediately.
   * The engine also calls this automatically from the tick loop.
   * @returns {Uint8Array|null}  The encoded frame, or null if nothing changed
   */
  flush() {
    return this._sync.flush();
  }

  // ── Inbound data ──────────────────────────────────────────────────────────

  /**
   * Feed raw binary data from the network into the engine.
   * The frame is first buffered by the jitter buffer, then processed
   * in sequence-number order during the next tick.
   *
   * @param {Uint8Array|ArrayBuffer} rawData
   * @param {number} [seq]  Explicit sequence number (auto-detected from frame if omitted)
   * @param {number} [ts]   Explicit sender timestamp in ms (auto-detected if omitted)
   */
  receive(rawData, seq, ts) {
    const u8 = rawData instanceof ArrayBuffer ? new Uint8Array(rawData) : rawData;

    // Auto-detect seq and ts by peeking into the frame envelope
    if (seq === undefined || ts === undefined) {
      try {
        const peeked = BinarySerializer.decode(u8);
        seq = seq ?? (peeked.seq ?? (this._jitterBuffer._nextSeq ?? 0));
        ts  = ts  ?? (peeked.ts  ?? Date.now());
      } catch {
        seq = seq ?? (this._jitterBuffer._nextSeq ?? 0);
        ts  = ts  ?? Date.now();
      }
    }

    this._jitterBuffer.push({ seq, ts, data: u8 });
  }

  // ── Persistence ───────────────────────────────────────────────────────────

  /**
   * Snapshot the current merged state to persistent storage.
   * Overwrites any previous snapshot (history-less).
   */
  persist() {
    this._persistence.write(this._storageKey, this.getState());
  }

  /**
   * Restore the last persisted snapshot into local state.
   * @returns {boolean}  true if a snapshot was found and applied
   */
  restore() {
    const snapshot = this._persistence.read(this._storageKey);
    if (snapshot == null) return false;
    this._sync.set(snapshot);
    this._emit('restore', snapshot);
    return true;
  }

  /**
   * Delete the persisted snapshot for this engine instance.
   */
  clearPersisted() {
    this._persistence.clearAll();
  }

  // ── Event bus ─────────────────────────────────────────────────────────────

  /**
   * Subscribe to an engine event.
   *
   * Events:
   *   'connect'    — engine connected to a transport
   *   'disconnect' — engine disconnected from transport
   *   'update'     — remote state change applied (payload: merged state object)
   *   'restore'    — snapshot restored (payload: restored state object)
   *
   * @param   {string}   event
   * @param   {function} fn
   * @returns {this}     Chainable
   */
  on(event, fn) {
    if (!this._listeners.has(event)) this._listeners.set(event, new Set());
    this._listeners.get(event).add(fn);
    return this;
  }

  /**
   * Unsubscribe a listener.
   * @param   {string}   event
   * @param   {function} fn
   * @returns {this}
   */
  off(event, fn) {
    this._listeners.get(event)?.delete(fn);
    return this;
  }

  // ── Diagnostics ───────────────────────────────────────────────────────────

  /**
   * Return a live diagnostics snapshot from all subsystems.
   * @returns {{ jitter: object, sync: object }}
   */
  diagnostics() {
    return {
      jitter: {
        depth:       this._jitterBuffer.depth,
        jitterMs:    +this._jitterBuffer.jitter.toFixed(2),
        targetDelay: +this._jitterBuffer.targetDelay.toFixed(2),
        stats:       this._jitterBuffer.stats,
      },
      sync: {
        localSeq:  this._sync._localSeq,
        remoteSeq: this._sync._remoteSeq,
        unacked:   this._sync._unacked.size,
      },
    };
  }

  // ── Private internals ─────────────────────────────────────────────────────

  _startTick() {
    if (this._tickHandle !== null) return;
    this._tickHandle = setInterval(() => this._tick(), this._tickRate);
  }

  _stopTick() {
    if (this._tickHandle === null) return;
    clearInterval(this._tickHandle);
    this._tickHandle = null;
  }

  _tick() {
    // Process all jitter-buffer packets that are ready for playout
    const ready = this._jitterBuffer.drainReady();
    for (const pkt of ready) {
      this._sync.receive(pkt.data);
    }
  }

  _emit(event, payload) {
    this._listeners.get(event)?.forEach(fn => {
      try { fn(payload); } catch (err) {
        console.error(`[InteractionEngine] Uncaught error in '${event}' listener:`, err);
      }
    });
  }
}
