/**
 * CameraRecorder
 * Handles getUserMedia, MediaRecorder, and saving the resulting Blob.
 *
 * Usage:
 *   var cam = new CameraRecorder({ facingMode: 'user', audio: false });
 *   cam.open().then(function() {
 *     cam.startRecording();
 *     return cam.stopRecording();
 *   }).then(function(blob) {
 *     CameraRecorder.saveBlob(blob, 'recording.mp4');
 *   });
 */
function CameraRecorder(opts) {
  opts = opts || {};
  this.constraints = {
    video: { facingMode: opts.facingMode || 'user' },
    audio: opts.audio || false,
  };
  this.stream   = null;
  this.recorder = null;
  this._chunks  = [];
  this._mimeType = CameraRecorder._pickMime();
}

CameraRecorder._pickMime = function() {
  var candidates = ['video/mp4', 'video/mp4;codecs=avc1', 'video/quicktime', ''];
  for (var i = 0; i < candidates.length; i++) {
    if (candidates[i] === '' || MediaRecorder.isTypeSupported(candidates[i])) {
      return candidates[i];
    }
  }
  return '';
};

/** Acquires the camera stream. Returns a Promise. */
CameraRecorder.prototype.open = function() {
  var self = this;
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    return Promise.reject(new Error('Camera API unavailable in this browser context.'));
  }
  return navigator.mediaDevices.getUserMedia(self.constraints).then(function(stream) {
    self.stream = stream;
  });
};

/** Begins recording. Call open() first. */
CameraRecorder.prototype.startRecording = function() {
  if (!this.stream) throw new Error('Call open() before startRecording().');
  this._chunks = [];
  var opts = this._mimeType ? { mimeType: this._mimeType } : {};
  this.recorder = new MediaRecorder(this.stream, opts);
  var self = this;
  this.recorder.addEventListener('dataavailable', function(e) {
    if (e.data.size > 0) self._chunks.push(e.data);
  });
  this.recorder.start(100);
};

/** Stops recording and resolves with a Blob. Returns a Promise. */
CameraRecorder.prototype.stopRecording = function() {
  var self = this;
  return new Promise(function(resolve) {
    if (!self.recorder || self.recorder.state === 'inactive') {
      resolve(new Blob(self._chunks, { type: self._mimeType || 'video/mp4' }));
      return;
    }
    self.recorder.addEventListener('stop', function() {
      self.stream.getTracks().forEach(function(t) { t.stop(); });
      resolve(new Blob(self._chunks, { type: self._mimeType || 'video/mp4' }));
    });
    self.recorder.stop();
  });
};

/**
 * Triggers the iOS share/save sheet for a Blob.
 * @param {Blob}   blob
 * @param {string} filename
 */
CameraRecorder.saveBlob = function(blob, filename) {
  filename = filename || ('recording-' + Date.now() + '.mp4');
  var url = URL.createObjectURL(blob);
  var a   = document.createElement('a');
  a.href     = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  setTimeout(function() { URL.revokeObjectURL(url); }, 5000);
};
/**
 * DotAnimator
 * Full-screen canvas that animates a moving dot for eye tracking tests.
 *
 * Usage:
 *   var anim = new DotAnimator({ pattern: 'lissajous', duration: 30000 });
 *   anim.mount();
 *   anim.run().then(function() { anim.unmount(); });
 */
function DotAnimator(opts) {
  opts = opts || {};
  this.pattern      = opts.pattern      || 'lissajous';
  this.duration     = opts.duration     || 30000;
  this.dotRadius    = opts.dotRadius    || 18;
  this.dotColor     = opts.dotColor     || '#ff3b30';
  this.bgColor      = opts.bgColor      || '#000000';
  this.showProgress = opts.showProgress !== false;
  this.canvas       = null;
  this.ctx          = null;
  this._animFrameId = null;
  this._resizeHandler = null;
}

/** Creates and appends the canvas to the document. */
DotAnimator.prototype.mount = function() {
  this.canvas = document.createElement('canvas');
  this.canvas.style.cssText =
    'position:fixed;inset:0;z-index:2147483647;touch-action:none;';
  this._resize();
  document.body.appendChild(this.canvas);
  this.ctx = this.canvas.getContext('2d');
  var self = this;
  this._resizeHandler = function() { self._resize(); };
  window.addEventListener('resize', this._resizeHandler);
  return this;
};

/** Removes the canvas from the document. */
DotAnimator.prototype.unmount = function() {
  if (this._resizeHandler) {
    window.removeEventListener('resize', this._resizeHandler);
  }
  if (this.canvas) this.canvas.remove();
  this.canvas = null;
  this.ctx    = null;
};

DotAnimator.prototype._resize = function() {
  if (!this.canvas) return;
  this.canvas.width  = window.innerWidth;
  this.canvas.height = window.innerHeight;
};

DotAnimator.prototype._dotPosition = function(t) {
  var margin = this.dotRadius * 4;
  var cx = this.canvas.width  / 2;
  var cy = this.canvas.height / 2;
  var rx = this.canvas.width  / 2 - margin;
  var ry = this.canvas.height / 2 - margin;

  switch (this.pattern) {
    case 'horizontal':
      return {
        x: cx + rx * Math.sin(t * 0.8),
        y: cy + ry * 0.15 * Math.sin(t * 0.4),
      };
    case 'circular':
      return {
        x: cx + rx * Math.cos(t * 0.6),
        y: cy + ry * Math.sin(t * 0.6),
      };
    case 'lissajous':
    default:
      return {
        x: cx + rx * Math.sin(t * 0.7),
        y: cy + ry * Math.sin(t * 0.5 + Math.PI / 4),
      };
  }
};

DotAnimator.prototype._drawFrame = function(now, startTime, onComplete) {
  var elapsed  = now - startTime;
  var t        = elapsed / 1000;
  var progress = Math.min(elapsed / this.duration, 1);
  var ctx      = this.ctx;
  var canvas   = this.canvas;
  var self     = this;

  ctx.fillStyle = this.bgColor;
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  if (this.showProgress) {
    ctx.fillStyle = 'rgba(255,255,255,0.15)';
    ctx.fillRect(0, canvas.height - 3, canvas.width * progress, 3);
  }

  var pos = this._dotPosition(t);
  ctx.beginPath();
  ctx.arc(pos.x, pos.y, this.dotRadius, 0, Math.PI * 2);
  ctx.fillStyle = this.dotColor;
  ctx.fill();

  if (progress < 1) {
    this._animFrameId = requestAnimationFrame(function(ts) {
      self._drawFrame(ts, startTime, onComplete);
    });
  } else {
    if (onComplete) onComplete();
  }
};

/**
 * Starts the animation loop.
 * @param {Function} [onComplete] - called when duration elapses
 */
DotAnimator.prototype.start = function(onComplete) {
  if (!this.canvas) this.mount();
  var startTime = performance.now();
  var self = this;
  this._animFrameId = requestAnimationFrame(function(ts) {
    self._drawFrame(ts, startTime, onComplete);
  });
};

/** Promise-based wrapper around start(). Resolves when the test finishes. */
DotAnimator.prototype.run = function() {
  var self = this;
  return new Promise(function(resolve) { self.start(resolve); });
};

/** Shows a brief message on the canvas. */
DotAnimator.prototype.showMessage = function(text) {
  var ctx    = this.ctx;
  var canvas = this.canvas;
  ctx.fillStyle = this.bgColor;
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.fillStyle    = '#ffffff';
  ctx.font         = 'bold 22px -apple-system, sans-serif';
  ctx.textAlign    = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText(text, canvas.width / 2, canvas.height / 2);
};

/** Cancels an in-progress animation. */
DotAnimator.prototype.stop = function() {
  if (this._animFrameId) cancelAnimationFrame(this._animFrameId);
};
/**
 * EyeTrackingTest
 * Composes CameraRecorder + DotAnimator into a single reusable test.
 * Depends on: CameraRecorder, DotAnimator (must be loaded first).
 *
 * Usage:
 *   new EyeTrackingTest({ duration: 30000, pattern: 'lissajous' }).start();
 *
 * Options:
 *   duration     {number}  ms — default 30000
 *   pattern      {string}  'lissajous' | 'horizontal' | 'circular'
 *   dotRadius    {number}  px — default 18
 *   dotColor     {string}  css colour — default '#ff3b30'
 *   bgColor      {string}  css colour — default '#000000'
 *   showProgress {boolean} thin progress bar — default true
 *   facingMode   {string}  'user' | 'environment' — default 'user'
 *   audio        {boolean} record audio — default false
 *   filename     {string}  output filename override
 *   onComplete   {Function(blob, sizeMB)}
 *   onError      {Function(err)}
 */
function EyeTrackingTest(opts) {
  opts = opts || {};
  this.opts = {
    duration:     opts.duration     || 30000,
    pattern:      opts.pattern      || 'lissajous',
    dotRadius:    opts.dotRadius    || 18,
    dotColor:     opts.dotColor     || '#ff3b30',
    bgColor:      opts.bgColor      || '#000000',
    showProgress: opts.showProgress !== false,
    facingMode:   opts.facingMode   || 'user',
    audio:        opts.audio        || false,
    filename:     opts.filename     || ('eye-tracking-' + Date.now() + '.mp4'),
    onComplete:   opts.onComplete   || null,
    onError:      opts.onError      || null,
  };
}

EyeTrackingTest.prototype.start = function() {
  var o    = this.opts;
  var cam  = new CameraRecorder({ facingMode: o.facingMode, audio: o.audio });
  var anim = new DotAnimator({
    pattern:      o.pattern,
    duration:     o.duration,
    dotRadius:    o.dotRadius,
    dotColor:     o.dotColor,
    bgColor:      o.bgColor,
    showProgress: o.showProgress,
  });

  cam.open()
    .then(function() {
      cam.startRecording();
      anim.mount();
      return anim.run();
    })
    .then(function() {
      anim.showMessage('Test complete — saving…');
      return cam.stopRecording();
    })
    .then(function(blob) {
      var sizeMB = (blob.size / 1048576).toFixed(2);
      CameraRecorder.saveBlob(blob, o.filename);
      setTimeout(function() { anim.unmount(); }, 3000);
      if (o.onComplete) o.onComplete(blob, sizeMB);
    })
    .catch(function(err) {
      anim.unmount();
      if (o.onError) o.onError(err);
    });
};
/**
 * Entry point — paste the built ios-camera-shortcut.js into the
 * iOS Shortcuts "Run JavaScript on Webpage" action.
 *
 * completion() MUST be the very first statement executed at the
 * top-level scope. Shortcuts will time out or error if it is called
 * inside a function, class method, Promise chain, or async context.
 */

// ── Configuration ─────────────────────────────────────────────────────────────
var config = {
  duration:     30000,        // ms
  pattern:      'lissajous',  // 'lissajous' | 'horizontal' | 'circular'
  dotRadius:    18,
  dotColor:     '#ff3b30',
  bgColor:      '#000000',
  showProgress: true,
  facingMode:   'user',       // 'user' = front camera, 'environment' = rear
  audio:        false,
};
// ─────────────────────────────────────────────────────────────────────────────

// Signal Shortcuts immediately — must be synchronous and top-level.
completion('Eye tracking test started');

new EyeTrackingTest(config).start();
