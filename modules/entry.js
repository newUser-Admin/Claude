/**
 * Entry point — paste the built ios-camera-shortcut.js into the
 * iOS Shortcuts "Run JavaScript on Webpage" action.
 *
 * completion() must be called at the true top level of the script —
 * not inside any function, method, or Promise chain. The message is
 * built from config so it reflects what is actually executing.
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

// Top-level completion() — reflects the module and config executing.
completion('EyeTrackingTest: ' + config.pattern + ' ' + config.duration + 'ms ' + config.facingMode + ' camera');

new EyeTrackingTest(config).start();
