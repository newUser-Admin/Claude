/**
 * Entry point — paste the built ios-camera-shortcut.js into the
 * iOS Shortcuts "Run JavaScript on Webpage" action.
 *
 * completion() is injected as the first line of the built file by
 * build.sh — before any module code — so it always runs first.
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
