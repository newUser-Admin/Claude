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

  // Inline MIME detection — avoids static method assignment timing issues
  var candidates = ['video/mp4', 'video/mp4;codecs=avc1', 'video/quicktime', ''];
  this._mimeType = '';
  for (var i = 0; i < candidates.length; i++) {
    if (candidates[i] === '' ||
        (typeof MediaRecorder !== 'undefined' && MediaRecorder.isTypeSupported(candidates[i]))) {
      this._mimeType = candidates[i];
      break;
    }
  }
}

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
  // completion() reflects this module executing when used standalone.
  completion('CameraRecorder: recording started (' + this.constraints.video.facingMode + ' camera)');
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
