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
