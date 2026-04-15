/**
 * PRO-SPEC INTERACTION ENGINE
 * Components: Binary Protocol, Jitter Buffer, Memory Vault, and Network Relay.
 *
 * Architecture:
 *  - Binary Protocol  : ArrayBuffer-based serializer/deserializer for compact wire format
 *  - Jitter Buffer    : Priority-queue smoother that delays playback by a fixed window
 *  - Memory Vault     : In-process history-less event log (no localStorage / IndexedDB)
 *  - Network Relay    : Binary WebSocket transport with auto-reconnect awareness
 */

'use strict';

// ─────────────────────────────────────────────────────────────
// 1. MESSAGE TYPES
// ─────────────────────────────────────────────────────────────

const MSG_TYPE = Object.freeze({ KEY: 0, CLICK: 1, SYNC: 2 });

// ─────────────────────────────────────────────────────────────
// 2. BINARY PROTOCOL  (The Serializer)
//
//  Wire layout (13 bytes):
//   [0]      Uint8   – message type
//   [1..4]   Uint32  – relative timestamp (ms, big-endian)
//   [5..8]   Float32 – v1  (keyCode or x-percent)
//   [9..12]  Float32 – v2  (y-percent; 0 for key events)
// ─────────────────────────────────────────────────────────────

const Protocol = Object.freeze({
    BYTE_LENGTH: 13,

    /**
     * Serialize an event into a fixed-size ArrayBuffer.
     * @param {number} type   MSG_TYPE constant
     * @param {number} time   Relative timestamp in ms
     * @param {number} v1     Primary value (keyCode or x-percent)
     * @param {number} [v2=0] Secondary value (y-percent)
     * @returns {ArrayBuffer}
     */
    pack(type, time, v1, v2 = 0) {
        const buffer = new ArrayBuffer(Protocol.BYTE_LENGTH);
        const view   = new DataView(buffer);
        view.setUint8(0,   type);
        view.setUint32(1,  time);
        view.setFloat32(5, v1);
        view.setFloat32(9, v2);
        return buffer;
    },

    /**
     * Deserialize a raw ArrayBuffer into an event object.
     * @param {ArrayBuffer} buffer
     * @returns {{ type: number, t: number, v1: number, v2: number }}
     */
    unpack(buffer) {
        if (buffer.byteLength < Protocol.BYTE_LENGTH) {
            throw new RangeError(`Packet too short: ${buffer.byteLength} < ${Protocol.BYTE_LENGTH}`);
        }
        const view = new DataView(buffer);
        return {
            type: view.getUint8(0),
            t:    view.getUint32(1),
            v1:   view.getFloat32(5),
            v2:   view.getFloat32(9),
        };
    },
});

// ─────────────────────────────────────────────────────────────
// 3. JITTER BUFFER  (The Smoother)
//
//  Absorbs network jitter by delaying playback of incoming
//  events by a fixed window. Events are sorted by their
//  scheduled playback time before dispatch.
// ─────────────────────────────────────────────────────────────

class JitterBuffer {
    /** @type {Array<{playAt: number, type: number, t: number, v1: number, v2: number}>} */
    #queue = [];

    /** Smoothing delay in milliseconds. */
    #delay;

    /**
     * @param {number} [delayMs=100] Playback delay window in ms
     */
    constructor(delayMs = 100) {
        this.#delay = delayMs;
    }

    /**
     * Enqueue an event for future playback.
     * @param {{ type: number, t: number, v1: number, v2: number }} event
     */
    push(event) {
        const playAt = performance.now() + this.#delay;
        // Insert in sorted order (ascending playAt) to keep O(n) push cost low
        // for the typical case of near-monotonic arrivals.
        const entry = { ...event, playAt };
        let i = this.#queue.length;
        while (i > 0 && this.#queue[i - 1].playAt > playAt) i--;
        this.#queue.splice(i, 0, entry);
    }

    /**
     * Drain all events whose playAt ≤ now and dispatch them via callback.
     * Call this once per animation frame.
     * @param {(event: object) => void} callback
     */
    tick(callback) {
        const now = performance.now();
        while (this.#queue.length > 0 && this.#queue[0].playAt <= now) {
            callback(this.#queue.shift());
        }
    }

    /** Number of buffered events waiting for playback. */
    get size() { return this.#queue.length; }

    /** Discard all pending events. */
    flush() { this.#queue.length = 0; }
}

// ─────────────────────────────────────────────────────────────
// 4. INTERACTION ENGINE  (The Core)
// ─────────────────────────────────────────────────────────────

class InteractionEngine {
    // ── Private state ──────────────────────────────────────
    #memory    = [];          // Memory Vault: in-process event log
    #startTime = 0;           // Recording epoch (performance.now())
    #socket    = null;        // WebSocket relay handle
    #jitter    = new JitterBuffer(100);
    #rafHandle = null;        // requestAnimationFrame handle

    // ── Public flags ───────────────────────────────────────
    isRecording = false;
    isSyncing   = false;

    // ─────────────────────────────────────────────────────
    // 4a. Network Relay
    // ─────────────────────────────────────────────────────

    /**
     * Open a binary WebSocket connection to the relay server.
     * Incoming packets are unpacked and fed through the jitter buffer.
     *
     * @param {string} url  WebSocket URL (e.g. "ws://localhost:8080")
     */
    connect(url) {
        if (this.#socket) {
            this.#socket.close();
        }

        this.#socket = new WebSocket(url);
        this.#socket.binaryType = 'arraybuffer'; // essential for binary protocol

        this.#socket.onopen = () => {
            this.isSyncing = true;
            console.log(`[Engine] Connected to relay: ${url}`);
        };

        this.#socket.onmessage = ({ data }) => {
            try {
                const event = Protocol.unpack(data);
                this.#jitter.push(event);
            } catch (err) {
                console.warn('[Engine] Malformed packet:', err.message);
            }
        };

        this.#socket.onclose = () => {
            this.isSyncing = false;
            console.log('[Engine] Relay connection closed.');
        };

        this.#socket.onerror = (err) => {
            console.error('[Engine] WebSocket error:', err);
        };

        this.#startPlaybackTick();
    }

    /** Disconnect from the relay and stop the playback ticker. */
    disconnect() {
        this.#stopPlaybackTick();
        this.#socket?.close();
        this.#socket   = null;
        this.isSyncing = false;
    }

    // ─────────────────────────────────────────────────────
    // 4b. Playback Tick
    // ─────────────────────────────────────────────────────

    #startPlaybackTick() {
        if (this.#rafHandle !== null) return; // already running
        const step = () => {
            this.#jitter.tick((event) => this.executeAction(event));
            this.#rafHandle = requestAnimationFrame(step);
        };
        this.#rafHandle = requestAnimationFrame(step);
    }

    #stopPlaybackTick() {
        if (this.#rafHandle !== null) {
            cancelAnimationFrame(this.#rafHandle);
            this.#rafHandle = null;
        }
    }

    // ─────────────────────────────────────────────────────
    // 4c. Recording
    // ─────────────────────────────────────────────────────

    /** Begin a new recording session, clearing the Memory Vault. */
    start() {
        this.#memory    = [];
        this.#startTime = performance.now();
        this.isRecording = true;
        console.log('[Engine] Recording started.');
    }

    /** Pause recording without discarding the Memory Vault. */
    pause() {
        this.isRecording = false;
        console.log('[Engine] Recording paused.');
    }

    /** Stop recording. */
    stop() {
        this.isRecording = false;
        console.log(`[Engine] Recording stopped. ${this.#memory.length} events captured.`);
    }

    /**
     * Capture an interaction event, persist it to the Memory Vault,
     * and transmit it over the relay if connected.
     *
     * @param {number} type  MSG_TYPE constant
     * @param {number} v1    Primary value
     * @param {number} [v2]  Secondary value
     */
    log(type, v1, v2 = 0) {
        if (!this.isRecording) return;

        const t = Math.round(performance.now() - this.#startTime);

        // 1. Memory Vault: history-less in-process storage
        this.#memory.push({ type, t, v1, v2 });

        // 2. Binary Network Transmission
        if (this.#socket?.readyState === WebSocket.OPEN) {
            const packet = Protocol.pack(type, t, v1, v2);
            this.#socket.send(packet);
        }
    }

    // ─────────────────────────────────────────────────────
    // 4d. Local Replay
    // ─────────────────────────────────────────────────────

    /**
     * Replay the Memory Vault locally.
     *
     * @param {number} [speed=1]  Playback multiplier (2 = double speed)
     */
    replayLocal(speed = 1) {
        if (this.#memory.length === 0) {
            console.warn('[Engine] Nothing to replay.');
            return;
        }
        console.log(`[Engine] Replaying ${this.#memory.length} events at ${speed}x speed.`);
        this.#memory.forEach((entry) => {
            setTimeout(() => this.executeAction(entry), entry.t / speed);
        });
    }

    // ─────────────────────────────────────────────────────
    // 4e. Action Execution  (The "Ghost")
    // ─────────────────────────────────────────────────────

    /**
     * Execute a decoded event, driving app logic or visual feedback.
     * @param {{ type: number, v1: number, v2: number }} event
     */
    executeAction(event) {
        switch (event.type) {
            case MSG_TYPE.KEY: {
                const char = String.fromCharCode(event.v1);
                console.log(`[Engine] KeyPress → "${char}" (code ${event.v1})`);
                // Dispatch synthetic event or drive app logic here.
                break;
            }
            case MSG_TYPE.CLICK: {
                const x = event.v1 * window.innerWidth;
                const y = event.v2 * window.innerHeight;
                this.#drawVisualizer(x, y);
                break;
            }
            case MSG_TYPE.SYNC: {
                // Reserved for clock-sync or state-reconciliation frames.
                console.log(`[Engine] SYNC frame t=${event.t}`);
                break;
            }
            default:
                console.warn(`[Engine] Unknown event type: ${event.type}`);
        }
    }

    // ─────────────────────────────────────────────────────
    // 4f. Click Visualizer
    // ─────────────────────────────────────────────────────

    /**
     * Render a transient "ghost cursor" dot at (x, y).
     * @param {number} x  Absolute pixel X
     * @param {number} y  Absolute pixel Y
     */
    #drawVisualizer(x, y) {
        const dot = document.createElement('div');
        dot.style.cssText = [
            'position:fixed',
            `left:${x}px`,
            `top:${y}px`,
            'width:12px',
            'height:12px',
            'background:cyan',
            'border-radius:50%',
            'pointer-events:none',
            'transform:translate(-50%,-50%)',
            'z-index:9999',
            'transition:opacity 0.3s ease',
        ].join(';');

        document.body.appendChild(dot);

        // Fade-out then remove to avoid DOM bloat.
        setTimeout(() => {
            dot.style.opacity = '0';
            setTimeout(() => dot.remove(), 300);
        }, 200);
    }

    // ─────────────────────────────────────────────────────
    // 4g. Export / Diagnostics
    // ─────────────────────────────────────────────────────

    /** Serialise the Memory Vault to JSON for offline analysis. */
    export() {
        return JSON.stringify(this.#memory);
    }

    /** Snapshot of current engine state (read-only). */
    get stats() {
        return Object.freeze({
            events:      this.#memory.length,
            buffered:    this.#jitter.size,
            isRecording: this.isRecording,
            isSyncing:   this.isSyncing,
        });
    }
}

// ─────────────────────────────────────────────────────────────
// 5. BOOTSTRAP
// ─────────────────────────────────────────────────────────────

const Engine = new InteractionEngine();

// Uncomment to connect to a relay server:
// Engine.connect('ws://localhost:8080');

Engine.start();

// ── Global event listeners ────────────────────────────────────

window.addEventListener('keydown', (e) => {
    // Pack the first character's code; multi-char keys (e.g. "Enter") yield NaN,
    // which is safely stored as a Float32 NaN and round-trips correctly.
    Engine.log(MSG_TYPE.KEY, e.key.charCodeAt(0));
});

window.addEventListener('click', (e) => {
    // Quantise to viewport-relative percentages for resolution independence.
    const xPct = e.clientX / window.innerWidth;
    const yPct = e.clientY / window.innerHeight;
    Engine.log(MSG_TYPE.CLICK, xPct, yPct);
});
