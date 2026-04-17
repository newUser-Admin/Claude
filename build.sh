#!/bin/sh
# Concatenates the modules into a single file for pasting into the
# iOS Shortcuts "Run JavaScript on Webpage" action.
# Usage: sh build.sh
#
# completion() is called inside each module's start method as its
# first synchronous statement, so Shortcuts receives a result that
# reflects which module is actually executing.

OUT="ios-camera-shortcut.js"

cat modules/CameraRecorder.js  > "$OUT"
printf '\n'                    >> "$OUT"
cat modules/DotAnimator.js     >> "$OUT"
printf '\n'                    >> "$OUT"
cat modules/EyeTrackingTest.js >> "$OUT"
printf '\n'                    >> "$OUT"
cat modules/entry.js           >> "$OUT"

echo "Built $OUT ($(wc -l < $OUT) lines)"
