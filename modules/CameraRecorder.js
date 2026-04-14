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
