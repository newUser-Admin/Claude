/**
 * iOS 26 Shortcut — Eye Tracking Test (Smooth Pursuit)
 *
 * How to use:
 *  1. In Safari, open any page (even about:blank).
 *  2. In Shortcuts, add a "Run JavaScript on Webpage" action targeting that tab.
 *  3. Paste the contents of this file into the action.
 *
 * The script:
 *  - Records the user via the front camera (hidden — full screen is used for the test).
 *  - Displays a moving dot the user follows with their eyes.
 *  - Runs a configurable sequence of movement patterns.
 *  - Auto-saves the recording when the test completes.
 *
 * `completion()` is called immediately so the Shortcuts action doesn't time out.
 */

// ── Configuration ────────────────────────────────────────────────────────────
const CONFIG = {
  dotRadius: 18,          // px
  dotColor: '#ff3b30',    // red dot
  bgColor: '#000000',     // test background
  durationMs: 30_000,     // total test duration (ms)
  pattern: 'lissajous',   // 'lissajous' | 'horizontal' | 'circular'
};
// ─────────────────────────────────────────────────────────────────────────────

function runEyeTrackingTest() {
  if (!navigator.mediaDevices?.getUserMedia) {
    completion('Error: Camera API unavailable.');
    return;
  }

  const MIME_TYPES = ['video/mp4', 'video/mp4;codecs=avc1', 'video/quicktime', ''];
  const mimeType = MIME_TYPES.find((t) => t === '' || MediaRecorder.isTypeSupported(t)) ?? '';

  navigator.mediaDevices
    .getUserMedia({ video: { facingMode: 'user' }, audio: false })
    .then((stream) => {
      const chunks = [];
      const recorderOptions = mimeType ? { mimeType } : {};
      const recorder = new MediaRecorder(stream, recorderOptions);
      recorder.addEventListener('dataavailable', (e) => {
        if (e.data.size > 0) chunks.push(e.data);
      });
      recorder.start(100);

      /* ── Full-screen canvas overlay ── */
      const canvas = document.createElement('canvas');
      canvas.style.cssText =
        'position:fixed;inset:0;z-index:2147483647;touch-action:none;';
      canvas.width = window.innerWidth;
      canvas.height = window.innerHeight;
      document.body.appendChild(canvas);
      const ctx = canvas.getContext('2d');

      // Resize canvas if orientation changes
      window.addEventListener('resize', () => {
        canvas.width = window.innerWidth;
        canvas.height = window.innerHeight;
      });

      /* ── Dot animation ── */
      const startTime = performance.now();
      let animFrameId;

      function getDotPosition(t) {
        const w = canvas.width;
        const h = canvas.height;
        const margin = CONFIG.dotRadius * 4;
        const cx = w / 2, cy = h / 2;
        const rx = w / 2 - margin, ry = h / 2 - margin;

        switch (CONFIG.pattern) {
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
      }

      function drawFrame(now) {
        const elapsed = now - startTime;
        const t = elapsed / 1000; // seconds
        const progress = Math.min(elapsed / CONFIG.durationMs, 1);

        // Background
        ctx.fillStyle = CONFIG.bgColor;
        ctx.fillRect(0, 0, canvas.width, canvas.height);

        // Progress bar (subtle, bottom of screen)
        ctx.fillStyle = 'rgba(255,255,255,0.15)';
        ctx.fillRect(0, canvas.height - 3, canvas.width * progress, 3);

        // Dot
        const { x, y } = getDotPosition(t);
        ctx.beginPath();
        ctx.arc(x, y, CONFIG.dotRadius, 0, Math.PI * 2);
        ctx.fillStyle = CONFIG.dotColor;
        ctx.fill();

        if (progress < 1) {
          animFrameId = requestAnimationFrame(drawFrame);
        } else {
          finishTest();
        }
      }

      animFrameId = requestAnimationFrame(drawFrame);

      /* ── Finish: stop recording and auto-save ── */
      function finishTest() {
        recorder.addEventListener('stop', () => {
          stream.getTracks().forEach((t) => t.stop());

          const blob = new Blob(chunks, { type: mimeType || 'video/mp4' });
          const url = URL.createObjectURL(blob);
          const sizeMB = (blob.size / 1_048_576).toFixed(2);

          // Show brief "Saved" message on canvas before dismissing
          ctx.fillStyle = CONFIG.bgColor;
          ctx.fillRect(0, 0, canvas.width, canvas.height);
          ctx.fillStyle = '#fff';
          ctx.font = 'bold 22px -apple-system, sans-serif';
          ctx.textAlign = 'center';
          ctx.fillText('Test complete — saving recording…', canvas.width / 2, canvas.height / 2);

          // Auto-trigger download / share sheet
          const a = document.createElement('a');
          a.href = url;
          a.download = `eye-tracking-${Date.now()}.mp4`;
          document.body.appendChild(a);
          a.click();
          document.body.removeChild(a);

          setTimeout(() => {
            URL.revokeObjectURL(url);
            canvas.remove();
          }, 3000);
        });

        recorder.stop();
      }

      // Signal Shortcuts immediately so the action doesn't time out
      completion('Eye tracking test started');
    })
    .catch((err) => {
      completion('Error: ' + err.message);
    });
}

runEyeTrackingTest();
