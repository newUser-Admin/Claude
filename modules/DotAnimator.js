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
