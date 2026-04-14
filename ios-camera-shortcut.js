/**
 * iOS 26 Shortcut — Front Camera Auto-Record
 *
 * How to use:
 *  1. In Safari, open any page (even about:blank).
 *  2. In Shortcuts, add a "Run JavaScript on Webpage" action targeting that tab.
 *  3. Paste the contents of this file into the action.
 *
 * On run the script will:
 *  - Open the front (selfie) camera.
 *  - Immediately start recording with audio.
 *  - Show a live preview and a "Stop & Save" button.
 *  - When stopped, offer a download link for the recorded MP4.
 *
 * `completion()` is the Shortcuts-provided callback that signals the action
 * is done and optionally passes a string to the next Shortcut step.
 */

function openCameraAndRecord() {
  if (!navigator.mediaDevices?.getUserMedia) {
    completion('Error: Camera API unavailable in this browser context.');
    return;
  }

  // Pick the best supported MIME type — iOS Safari supports mp4, not webm
  const MIME_TYPES = ['video/mp4', 'video/mp4;codecs=avc1', 'video/quicktime', ''];
  const mimeType = MIME_TYPES.find((t) => t === '' || MediaRecorder.isTypeSupported(t)) ?? '';

  navigator.mediaDevices
    .getUserMedia({
      video: { facingMode: 'user' }, // front (selfie) camera
      audio: true,
    })
    .then((stream) => {
      const chunks = [];

      /* ── MediaRecorder — starts immediately ── */
      const recorderOptions = mimeType ? { mimeType } : {};
      const recorder = new MediaRecorder(stream, recorderOptions);
      recorder.addEventListener('dataavailable', (e) => {
        if (e.data.size > 0) chunks.push(e.data);
      });
      recorder.start(100); // collect a chunk every 100 ms

      /* ── Overlay container ── */
      const overlay = document.createElement('div');
      overlay.style.cssText =
        'position:fixed;inset:0;background:#000;z-index:2147483647;' +
        'display:flex;flex-direction:column;align-items:center;justify-content:center;';

      /* ── Live preview ── */
      const video = document.createElement('video');
      video.style.cssText = 'width:100%;height:100%;object-fit:cover;';
      video.setAttribute('playsinline', ''); // required for inline playback on iOS
      video.setAttribute('autoplay', '');
      video.muted = true; // mute preview to avoid echo; audio is still recorded
      video.srcObject = stream;

      /* ── Stop & Save button ── */
      const stopBtn = document.createElement('button');
      stopBtn.textContent = 'Stop & Save';
      stopBtn.style.cssText =
        'position:absolute;top:env(safe-area-inset-top,20px);right:16px;margin-top:12px;' +
        'padding:10px 22px;background:rgba(255,255,255,0.85);backdrop-filter:blur(8px);' +
        'border:none;border-radius:20px;font-size:16px;font-weight:600;cursor:pointer;';

      stopBtn.addEventListener('click', () => {
        recorder.addEventListener('stop', () => {
          // Stop camera tracks
          stream.getTracks().forEach((t) => t.stop());

          // Build a blob from recorded chunks
          const blob = new Blob(chunks, { type: mimeType || 'video/mp4' });
          const url = URL.createObjectURL(blob);

          // Replace the live preview with a playback + download UI
          overlay.innerHTML = '';
          overlay.style.background = '#111';

          const playback = document.createElement('video');
          playback.src = url;
          playback.controls = true;
          playback.setAttribute('playsinline', '');
          playback.style.cssText = 'max-width:100%;max-height:75vh;margin-top:40px;';

          const sizeMB = (blob.size / 1_048_576).toFixed(2);
          const info = document.createElement('p');
          info.textContent = `Recording ready — ${sizeMB} MB`;
          info.style.cssText = 'color:#fff;font-size:15px;margin:12px 0;';

          const downloadBtn = document.createElement('a');
          downloadBtn.href = url;
          downloadBtn.download = `recording-${Date.now()}.mp4`;
          downloadBtn.textContent = 'Download / Share';
          downloadBtn.style.cssText =
            'display:inline-block;padding:12px 28px;background:#0a84ff;color:#fff;' +
            'text-decoration:none;border-radius:20px;font-size:16px;font-weight:600;margin:8px;';

          const dismissBtn = document.createElement('button');
          dismissBtn.textContent = 'Dismiss';
          dismissBtn.style.cssText =
            'padding:12px 28px;background:rgba(255,255,255,0.15);color:#fff;' +
            'border:none;border-radius:20px;font-size:16px;cursor:pointer;margin:8px;';
          dismissBtn.addEventListener('click', () => {
            URL.revokeObjectURL(url);
            overlay.remove();
            completion('Recording saved: ' + sizeMB + ' MB');
          });

          overlay.appendChild(playback);
          overlay.appendChild(info);
          overlay.appendChild(downloadBtn);
          overlay.appendChild(dismissBtn);
        });

        recorder.stop();
      });

      /* ── Assemble and inject ── */
      overlay.appendChild(video);
      overlay.appendChild(stopBtn);
      document.body.appendChild(overlay);

      video.play().catch(() => {
        // Autoplay blocked — prompt a tap to begin
        const playBtn = document.createElement('button');
        playBtn.textContent = 'Tap to Start';
        playBtn.style.cssText =
          'position:absolute;padding:14px 28px;background:rgba(255,255,255,0.9);' +
          'border:none;border-radius:14px;font-size:18px;font-weight:700;cursor:pointer;';
        playBtn.addEventListener('click', () => {
          video.play();
          playBtn.remove();
        });
        overlay.appendChild(playBtn);
      });
    })
    .catch((err) => {
      completion('Error accessing camera/microphone: ' + err.message);
    });
}

openCameraAndRecord();
