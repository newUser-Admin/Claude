#!/bin/sh
# Concatenates the modules into a single file for pasting into the
# iOS Shortcuts "Run JavaScript on Webpage" action.
# Usage: sh build.sh

OUT="ios-camera-shortcut.js"

# completion() MUST be the absolute first line of the built file.
# Shortcuts only recognises it when called at the top-level scope,
# before any module code has a chance to parse or throw.
printf 'completion("Eye tracking test started");\n\n' > "$OUT"

cat modules/CameraRecorder.js  >> "$OUT"
printf '\n'                    >> "$OUT"
cat modules/DotAnimator.js     >> "$OUT"
printf '\n'                    >> "$OUT"
cat modules/EyeTrackingTest.js >> "$OUT"
printf '\n'                    >> "$OUT"
cat modules/entry.js           >> "$OUT"

echo "Built $OUT ($(wc -l < $OUT) lines)"
