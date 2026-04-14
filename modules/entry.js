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
