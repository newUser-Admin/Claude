#!/bin/sh
# Concatenates the modules into a single file for pasting into the
# iOS Shortcuts "Run JavaScript on Webpage" action.
# Usage: sh build.sh

OUT="ios-camera-shortcut.js"

cat modules/CameraRecorder.js \
    modules/DotAnimator.js \
    modules/EyeTrackingTest.js \
    modules/entry.js \
    > "$OUT"

echo "Built $OUT"
