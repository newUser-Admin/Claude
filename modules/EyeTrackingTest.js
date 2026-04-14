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
  // completion() is the first synchronous statement so Shortcuts receives
  // a result that reflects this specific module executing.
  completion('EyeTrackingTest: running (' + this.opts.pattern + ', ' + this.opts.duration + 'ms)');

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
