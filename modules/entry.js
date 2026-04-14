/**
 * Entry point — paste the built ios-camera-shortcut.js into the
 * iOS Shortcuts "Run JavaScript on Webpage" action.
 *
 * completion() MUST be the very first statement executed at the
 * top-level scope. Shortcuts will time out or error if it is called
 * inside a function, class method, Promise chain, or async context.
 */

// ── Configuration ─────────────────────────────────────────────────────────────
var config = {
  duration:     30000,        // ms
  pattern:      'lissajous',  // 'lissajous' | 'horizontal' | 'circular'
  dotRadius:    18,
  dotColor:     '#ff3b30',
  bgColor:      '#000000',
  showProgress: true,
  facingMode:   'user',       // 'user' = front camera, 'environment' = rear
  audio:        false,
};
// ─────────────────────────────────────────────────────────────────────────────

// Signal Shortcuts immediately — must be synchronous and top-level.
completion('Eye tracking test started');

new EyeTrackingTest(config).start();
