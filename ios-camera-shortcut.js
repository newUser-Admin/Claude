/**
 * iOS 26 Shortcut — Modular Eye Tracking Test
 *
 * How to use:
 *  1. In Safari, open any page (even about:blank).
 *  2. In Shortcuts, add a "Run JavaScript on Webpage" action targeting that tab.
 *  3. Paste the contents of this file into the action.
 *
 * Architecture:
 *  - CameraRecorder  — manages the camera stream and MediaRecorder
 *  - DotAnimator     — manages the full-screen canvas and dot movement
 *  - EyeTrackingTest — composes the two; the only class you need to instantiate
 *
 * Quick-start (bottom of file):
 *  new EyeTrackingTest({ duration: 30_000, pattern: 'lissajous' }).start();
 */

// ── CameraRecorder ────────────────────────────────────────────────────────────
/**
 * Wraps getUserMedia + MediaRecorder.
 *
 * Usage:
 *   const cam = new CameraRecorder({ facingMode: 'user', audio: false });
 *   await cam.open();
 *   cam.startRecording();
 *   const blob = await cam.stopRecording();
 */
class CameraRecorder {
  /**
   * @param {object} opts
   * @param {'user'|'environment'} [opts.facingMode='user']
   * @param {boolean}              [opts.audio=false]
   */
  constructor({ facingMode = 'user', audio = false } = {}) {
    this.constraints = { video: { facingMode }, audio };
    this.stream = null;
    this.recorder = null;
    this._chunks = [];
    this._mimeType = this._pickMime();
  }

  _pickMime() {
    const candidates = ['video/mp4', 'video/mp4;codecs=avc1', 'video/quicktime', ''];
    return candidates.find((t) => t === '' || MediaRecorder.isTypeSupported(t)) ?? '';
  }

  /** Acquires the camera stream. Must be called before startRecording(). */
  async open() {
    if (!navigator.mediaDevices?.getUserMedia) {
      throw new Error('Camera API unavailable in this browser context.');
    }
    this.stream = await navigator.mediaDevices.getUserMedia(this.constraints);
    return this;
  }

  /** Begins recording. Call open() first. */
  startRecording() {
    if (!this.stream) throw new Error('Call open() before startRecording().');
    this._chunks = [];
    const opts = this._mimeType ? { mimeType: this._mimeType } : {};
    this.recorder = new MediaRecorder(this.stream, opts);
    this.recorder.addEventListener('dataavailable', (e) => {
      if (e.data.size > 0) this._chunks.push(e.data);
    });
    this.recorder.start(100); // chunk every 100 ms
  }

  /**
   * Stops recording and returns a Blob.
   * @returns {Promise<Blob>}
   */
  stopRecording() {
    return new Promise((resolve) => {
      if (!this.recorder || this.recorder.state === 'inactive') {
        resolve(new Blob(this._chunks, { type: this._mimeType || 'video/mp4' }));
        return;
      }
      this.recorder.addEventListener('stop', () => {
        this.stream.getTracks().forEach((t) => t.stop());
        resolve(new Blob(this._chunks, { type: this._mimeType || 'video/mp4' }));
      });
      this.recorder.stop();
    });
  }

  /**
   * Triggers the iOS share/save sheet for a recorded Blob.
   * @param {Blob}   blob
   * @param {string} [filename]
   */
  static saveBlob(blob, filename = `recording-${Date.now()}.mp4`) {
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 5000);
  }
}


// ── DotAnimator ───────────────────────────────────────────────────────────────
/**
 * Full-screen canvas that animates a moving dot.
 *
 * Usage:
 *   const anim = new DotAnimator({ pattern: 'lissajous', duration: 30_000 });
 *   anim.mount();
 *   anim.start(() => console.log('done'));
 *   // or: await anim.run();
 */
class DotAnimator {
  /**
   * @param {object}  opts
   * @param {string}  [opts.pattern='lissajous'] — 'lissajous' | 'horizontal' | 'circular'
   * @param {number}  [opts.duration=30000]       — ms
   * @param {number}  [opts.dotRadius=18]         — px
   * @param {string}  [opts.dotColor='#ff3b30']
   * @param {string}  [opts.bgColor='#000000']
   * @param {boolean} [opts.showProgress=true]    — thin progress bar at bottom
   */
  constructor({
    pattern = 'lissajous',
    duration = 30_000,
    dotRadius = 18,
    dotColor = '#ff3b30',
    bgColor = '#000000',
    showProgress = true,
  } = {}) {
    this.pattern = pattern;
    this.duration = duration;
    this.dotRadius = dotRadius;
    this.dotColor = dotColor;
    this.bgColor = bgColor;
    this.showProgress = showProgress;
    this.canvas = null;
    this.ctx = null;
    this._animFrameId = null;
    this._resizeHandler = null;
  }

  /** Creates and appends the canvas to the document. */
  mount() {
    this.canvas = document.createElement('canvas');
    this.canvas.style.cssText =
      'position:fixed;inset:0;z-index:2147483647;touch-action:none;';
    this._resize();
    document.body.appendChild(this.canvas);
    this.ctx = this.canvas.getContext('2d');
    this._resizeHandler = () => this._resize();
    window.addEventListener('resize', this._resizeHandler);
    return this;
  }

  /** Removes the canvas from the document. */
  unmount() {
    if (this._resizeHandler) window.removeEventListener('resize', this._resizeHandler);
    this.canvas?.remove();
    this.canvas = null;
    this.ctx = null;
  }

  _resize() {
    if (!this.canvas) return;
    this.canvas.width = window.innerWidth;
    this.canvas.height = window.innerHeight;
  }

  _dotPosition(t) {
    const { canvas, dotRadius, pattern } = this;
    const margin = dotRadius * 4;
    const cx = canvas.width / 2,  cy = canvas.height / 2;
    const rx = canvas.width / 2  - margin;
    const ry = canvas.height / 2 - margin;

    switch (pattern) {
      case 'horizontal':
        return { x: cx + rx * Math.sin(t * 0.8), y: cy + ry * 0.15 * Math.sin(t * 0.4) };
      case 'circular':
        return { x: cx + rx * Math.cos(t * 0.6), y: cy + ry * Math.sin(t * 0.6) };
      case 'lissajous':
      default:
        return { x: cx + rx * Math.sin(t * 0.7), y: cy + ry * Math.sin(t * 0.5 + Math.PI / 4) };
    }
  }

  _drawFrame(now, startTime, onComplete) {
    const elapsed  = now - startTime;
    const t        = elapsed / 1000;
    const progress = Math.min(elapsed / this.duration, 1);
    const { ctx, canvas } = this;

    ctx.fillStyle = this.bgColor;
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    if (this.showProgress) {
      ctx.fillStyle = 'rgba(255,255,255,0.15)';
      ctx.fillRect(0, canvas.height - 3, canvas.width * progress, 3);
    }

    const { x, y } = this._dotPosition(t);
    ctx.beginPath();
    ctx.arc(x, y, this.dotRadius, 0, Math.PI * 2);
    ctx.fillStyle = this.dotColor;
    ctx.fill();

    if (progress < 1) {
      this._animFrameId = requestAnimationFrame((ts) => this._drawFrame(ts, startTime, onComplete));
    } else {
      onComplete?.();
    }
  }

  /**
   * Starts the animation loop.
   * @param {Function} [onComplete] — called when duration elapses
   */
  start(onComplete) {
    if (!this.canvas) this.mount();
    const startTime = performance.now();
    this._animFrameId = requestAnimationFrame((ts) => this._drawFrame(ts, startTime, onComplete));
  }

  /** Promise-based alternative to start(). Resolves when the test finishes. */
  run() {
    return new Promise((resolve) => this.start(resolve));
  }

  /** Shows a brief message on the canvas (e.g. after test completes). */
  showMessage(text) {
    const { ctx, canvas } = this;
    ctx.fillStyle = this.bgColor;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.fillStyle = '#ffffff';
    ctx.font = 'bold 22px -apple-system, sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(text, canvas.width / 2, canvas.height / 2);
  }

  /** Cancels an in-progress animation. */
  stop() {
    if (this._animFrameId) cancelAnimationFrame(this._animFrameId);
  }
}


// ── EyeTrackingTest ───────────────────────────────────────────────────────────
/**
 * Composes CameraRecorder + DotAnimator into a single reusable test.
 *
 * Usage:
 *   const test = new EyeTrackingTest({ duration: 30_000, pattern: 'lissajous' });
 *   test.start();
 *
 * All DotAnimator and CameraRecorder options can be passed directly.
 */
class EyeTrackingTest {
  /**
   * @param {object}  opts
   * @param {number}  [opts.duration=30000]
   * @param {string}  [opts.pattern='lissajous']
   * @param {number}  [opts.dotRadius=18]
   * @param {string}  [opts.dotColor='#ff3b30']
   * @param {string}  [opts.bgColor='#000000']
   * @param {boolean} [opts.showProgress=true]
   * @param {'user'|'environment'} [opts.facingMode='user']
   * @param {boolean}  [opts.audio=false]
   * @param {string}   [opts.filename]         — override output filename
   * @param {Function} [opts.onComplete]       — called with the saved Blob
   * @param {Function} [opts.onError]          — called with an Error
   */
  constructor(opts = {}) {
    this.opts = {
      duration: 30_000,
      pattern: 'lissajous',
      dotRadius: 18,
      dotColor: '#ff3b30',
      bgColor: '#000000',
      showProgress: true,
      facingMode: 'user',
      audio: false,
      filename: `eye-tracking-${Date.now()}.mp4`,
      onComplete: null,
      onError: null,
      ...opts,
    };
  }

  start() {
    const { duration, pattern, dotRadius, dotColor, bgColor, showProgress,
            facingMode, audio, filename, onComplete, onError } = this.opts;

    const cam  = new CameraRecorder({ facingMode, audio });
    const anim = new DotAnimator({ pattern, duration, dotRadius, dotColor, bgColor, showProgress });

    // completion() is called at the top-level scope below, not here.

    cam.open()
      .then(() => {
        cam.startRecording();
        anim.mount();
        return anim.run(); // resolves when duration elapses
      })
      .then(() => {
        anim.showMessage('Test complete — saving…');
        return cam.stopRecording();
      })
      .then((blob) => {
        const sizeMB = (blob.size / 1_048_576).toFixed(2);
        CameraRecorder.saveBlob(blob, filename);
        setTimeout(() => anim.unmount(), 3000);
        onComplete?.(blob, sizeMB);
      })
      .catch((err) => {
        anim.unmount();
        onError?.(err);
      });
  }
}


// ── Run ───────────────────────────────────────────────────────────────────────
// completion() MUST be called at the top-level script scope — Shortcuts does
// not recognise it when called from inside a class method or Promise chain.
completion('Eye tracking test started');

new EyeTrackingTest({
  duration:     30_000,       // ms
  pattern:      'lissajous',  // 'lissajous' | 'horizontal' | 'circular'
  dotRadius:    18,
  dotColor:     '#ff3b30',
  bgColor:      '#000000',
  showProgress: true,
  facingMode:   'user',       // 'user' = front, 'environment' = rear
  audio:        false,
}).start();
