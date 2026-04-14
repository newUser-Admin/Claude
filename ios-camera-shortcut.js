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
  var self = this;

  self.constraints = {
    video: { facingMode: opts.facingMode || 'user' },
    audio: opts.audio || false,
  };
  self.stream   = null;
  self.recorder = null;
  self._chunks  = [];

  // Inline MIME detection
  var candidates = ['video/mp4', 'video/mp4;codecs=avc1', 'video/quicktime', ''];
  self._mimeType = '';
  for (var i = 0; i < candidates.length; i++) {
    if (candidates[i] === '' ||
        (typeof MediaRecorder !== 'undefined' && MediaRecorder.isTypeSupported(candidates[i]))) {
      self._mimeType = candidates[i];
      break;
    }
  }

  /** Acquires the camera stream. Returns a Promise. */
  self.open = function() {
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      return Promise.reject(new Error('Camera API unavailable in this browser context.'));
    }
    return navigator.mediaDevices.getUserMedia(self.constraints).then(function(stream) {
      self.stream = stream;
    });
  };

  /** Begins recording. Call open() first. */
  self.startRecording = function() {
    if (!self.stream) throw new Error('Call open() before startRecording().');
    // completion() reflects this module executing when used standalone.
    completion('CameraRecorder: recording started (' + self.constraints.video.facingMode + ' camera)');
    self._chunks = [];
    var recOpts = self._mimeType ? { mimeType: self._mimeType } : {};
    self.recorder = new MediaRecorder(self.stream, recOpts);
    self.recorder.addEventListener('dataavailable', function(e) {
      if (e.data.size > 0) self._chunks.push(e.data);
    });
    self.recorder.start(100);
  };

  /** Stops recording and resolves with a Blob. Returns a Promise. */
  self.stopRecording = function() {
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
}

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
  var self = this;

  self.pattern      = opts.pattern      || 'lissajous';
  self.duration     = opts.duration     || 30000;
  self.dotRadius    = opts.dotRadius    || 18;
  self.dotColor     = opts.dotColor     || '#ff3b30';
  self.bgColor      = opts.bgColor      || '#000000';
  self.showProgress = opts.showProgress !== false;
  self.canvas       = null;
  self.ctx          = null;
  self._animFrameId = null;

  /** Creates and appends the canvas to the document. */
  self.mount = function() {
    self.canvas = document.createElement('canvas');
    self.canvas.style.cssText =
      'position:fixed;top:0;left:0;width:100%;height:100%;z-index:2147483647;touch-action:none;';
    self.canvas.width  = window.innerWidth;
    self.canvas.height = window.innerHeight;
    document.body.appendChild(self.canvas);
    self.ctx = self.canvas.getContext('2d');
    window.addEventListener('resize', self._resize);
    return self;
  };

  /** Removes the canvas from the document. */
  self.unmount = function() {
    window.removeEventListener('resize', self._resize);
    if (self.canvas) self.canvas.remove();
    self.canvas = null;
    self.ctx    = null;
  };

  self._resize = function() {
    if (!self.canvas) return;
    self.canvas.width  = window.innerWidth;
    self.canvas.height = window.innerHeight;
  };

  self._dotPosition = function(t) {
    var margin = self.dotRadius * 4;
    var cx = self.canvas.width  / 2;
    var cy = self.canvas.height / 2;
    var rx = self.canvas.width  / 2 - margin;
    var ry = self.canvas.height / 2 - margin;

    switch (self.pattern) {
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

  self._drawFrame = function(now, startTime, onComplete) {
    var elapsed  = now - startTime;
    var t        = elapsed / 1000;
    var progress = Math.min(elapsed / self.duration, 1);

    self.ctx.fillStyle = self.bgColor;
    self.ctx.fillRect(0, 0, self.canvas.width, self.canvas.height);

    if (self.showProgress) {
      self.ctx.fillStyle = 'rgba(255,255,255,0.15)';
      self.ctx.fillRect(0, self.canvas.height - 3, self.canvas.width * progress, 3);
    }

    var pos = self._dotPosition(t);
    self.ctx.beginPath();
    self.ctx.arc(pos.x, pos.y, self.dotRadius, 0, Math.PI * 2);
    self.ctx.fillStyle = self.dotColor;
    self.ctx.fill();

    if (progress < 1) {
      self._animFrameId = requestAnimationFrame(function(ts) {
        self._drawFrame(ts, startTime, onComplete);
      });
    } else {
      if (onComplete) onComplete();
    }
  };

  /** Starts the animation loop. */
  self.start = function(onComplete) {
    if (!self.canvas) self.mount();
    var startTime = performance.now();
    self._animFrameId = requestAnimationFrame(function(ts) {
      self._drawFrame(ts, startTime, onComplete);
    });
  };

  /** Promise-based wrapper. Resolves when the test finishes. */
  self.run = function() {
    return new Promise(function(resolve) { self.start(resolve); });
  };

  /** Shows a brief message on the canvas. */
  self.showMessage = function(text) {
    self.ctx.fillStyle = self.bgColor;
    self.ctx.fillRect(0, 0, self.canvas.width, self.canvas.height);
    self.ctx.fillStyle    = '#ffffff';
    self.ctx.font         = 'bold 22px -apple-system, sans-serif';
    self.ctx.textAlign    = 'center';
    self.ctx.textBaseline = 'middle';
    self.ctx.fillText(text, self.canvas.width / 2, self.canvas.height / 2);
  };

  /** Cancels an in-progress animation. */
  self.stop = function() {
    if (self._animFrameId) cancelAnimationFrame(self._animFrameId);
  };
}

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
  var self = this;

  self.opts = {
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

  self.start = function() {
    var o = self.opts;

    // completion() is the first synchronous statement so Shortcuts receives
    // a result that reflects this specific module executing.
    completion('EyeTrackingTest: running (' + o.pattern + ', ' + o.duration + 'ms)');

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
}

/**
 * Entry point — paste the built ios-camera-shortcut.js into the
 * iOS Shortcuts "Run JavaScript on Webpage" action.
 *
 * completion() is called by each module as its first synchronous
 * statement, reflecting which module is actually executing.
 */

// ── Configuration ─────────────────────────────────────────────────────────────
var config = {
  duration:     30000,
  pattern:      'lissajous',
  dotRadius:    18,
  dotColor:     '#ff3b30',
  bgColor:      '#000000',
  showProgress: true,
  facingMode:   'user',
  audio:        false,
};
// ─────────────────────────────────────────────────────────────────────────────

new EyeTrackingTest(config).start();
