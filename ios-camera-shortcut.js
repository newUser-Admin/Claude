/**
 * iOS 26 Shortcut — Open Camera
 *
 * How to use:
 *  1. In the Shortcuts app, add a "Get Current Page" action (to have a Safari
 *     page available) or navigate to any webpage in Safari first.
 *  2. Add a "Run JavaScript on Webpage" action.
 *  3. Paste the contents of this file into that action.
 *
 * The script injects a full-screen camera overlay into the current Safari page.
 * Tapping "Close" stops the stream and removes the overlay.
 *
 * `completion()` is the Shortcuts-provided callback that ends the JavaScript
 * action and optionally passes a string value to the next Shortcut step.
 */

function openCamera() {
  // 'environment' = rear camera. Change to 'user' for the front camera.
  const constraints = {
    video: { facingMode: 'environment' },
    audio: false,
  };

  if (!navigator.mediaDevices?.getUserMedia) {
    completion('Error: Camera API unavailable in this browser context.');
    return;
  }

  navigator.mediaDevices
    .getUserMedia(constraints)
    .then((stream) => {
      /* ── Overlay container ── */
      const overlay = document.createElement('div');
      overlay.style.cssText =
        'position:fixed;inset:0;background:#000;z-index:2147483647;' +
        'display:flex;flex-direction:column;align-items:center;justify-content:center;';

      /* ── Video element ── */
      const video = document.createElement('video');
      video.style.cssText = 'width:100%;height:100%;object-fit:cover;';
      // Both attributes are required for inline autoplay on iOS Safari
      video.setAttribute('playsinline', '');
      video.setAttribute('autoplay', '');
      video.muted = true;
      video.srcObject = stream;

      /* ── Close button ── */
      const closeBtn = document.createElement('button');
      closeBtn.textContent = 'Close';
      closeBtn.style.cssText =
        'position:absolute;top:env(safe-area-inset-top,20px);right:16px;' +
        'margin-top:12px;padding:10px 22px;' +
        'background:rgba(255,255,255,0.85);backdrop-filter:blur(8px);' +
        'border:none;border-radius:20px;font-size:16px;font-weight:600;' +
        'cursor:pointer;z-index:2147483647;';

      closeBtn.addEventListener('click', () => {
        stream.getTracks().forEach((track) => track.stop());
        overlay.remove();
        completion('Camera closed by user.');
      });

      /* ── Assemble and inject ── */
      overlay.appendChild(video);
      overlay.appendChild(closeBtn);
      document.body.appendChild(overlay);

      video.play().catch((err) => {
        // Autoplay was blocked — show a tap-to-start prompt
        const playBtn = document.createElement('button');
        playBtn.textContent = 'Tap to Start Camera';
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
      // Common causes: user denied permission, or no camera hardware
      completion('Error opening camera: ' + err.message);
    });
}

openCamera();
