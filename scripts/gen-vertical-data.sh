#!/usr/bin/env bash
#
# Generate a vertical_data file for the 9:16 vertical crop, and print an exc
# command line that uses it.
#
# vertical_data is one little-endian uint32 per frame: the crop window centre as
# a fraction of the scaled frame width, with a fixed denominator of 10000
# (0 = left edge, 5000 = centre, 10000 = right edge). See VERTICAL_DATA_SCALE in
# libavpipe/include/avpipe_utils.h.
#
# The patterns move the window in ways that are obvious on playback, so a crop
# that lags, sticks or jumps is visible. libavpipe/test/test_vertical_crop.c
# checks the same pingpong shape numerically.
#
# Usage: gen-vertical-data.sh [options]
#   -p pattern   pingpong (default), sweep, hold, edges
#   -n frames    number of frames (default 180)
#   -o file      output file (default <pattern>.bin)
#   -H height    encoder height, for the crop width in the printed command (default 360)
#   -f fps       source frame rate, for -duration-ts (default 30)
#   -b timebase  source timebase denominator, for -duration-ts (default 15360)
#   -s source    source file to name in the printed command (default <source>)
#   -w width     scaled frame width; enables the per-frame crop_x table
#   -t           print the per-frame table (needs -w)
#
set -euo pipefail

SCALE=10000
PATTERN=pingpong
FRAMES=180
OUT=
HEIGHT=360
FPS=30
TIMEBASE=15360
SOURCE='<source>'
SCALED_W=0
TABLE=0

usage() { sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while getopts "p:n:o:H:f:b:s:w:th" opt; do
    case $opt in
        p) PATTERN=$OPTARG ;;
        n) FRAMES=$OPTARG ;;
        o) OUT=$OPTARG ;;
        H) HEIGHT=$OPTARG ;;
        f) FPS=$OPTARG ;;
        b) TIMEBASE=$OPTARG ;;
        s) SOURCE=$OPTARG ;;
        w) SCALED_W=$OPTARG ;;
        t) TABLE=1 ;;
        h) usage 0 ;;
        *) usage 1 ;;
    esac
done

case $PATTERN in
    pingpong|sweep|hold|edges) ;;
    *) echo "unknown pattern '$PATTERN' (want pingpong, sweep, hold or edges)" >&2; exit 1 ;;
esac
[ "$FRAMES" -gt 0 ] 2>/dev/null || { echo "-n must be a positive frame count" >&2; exit 1; }
[ "$TABLE" -eq 1 ] && [ "$SCALED_W" -eq 0 ] && { echo "-t needs -w <scaled width>" >&2; exit 1; }
OUT=${OUT:-$PATTERN.bin}

# Crop width: height * 9/16, rounded up to even (crop_calc_width in avpipe_filters.c).
CROP_W=$(( HEIGHT * 9 / 16 ))
[ $(( CROP_W % 2 )) -ne 0 ] && CROP_W=$(( CROP_W + 1 ))

# One awk program computes the pattern; mode picks the output. Keeping it in one
# place means the shapes cannot drift between the file and the table.
#
# pingpong is a triangle wave phase-shifted a quarter cycle, so each cycle starts
# and ends at centre - the same shape pan_value() uses in
# libavpipe/test/test_vertical_crop.c.
pattern_awk() {
    awk -v n="$FRAMES" -v pat="$PATTERN" -v scale="$SCALE" -v mode="$1" \
        -v w="$SCALED_W" -v cw="$CROP_W" '
    function frac(x) { return x - int(x) }
    function abs(x)  { return x < 0 ? -x : x }
    BEGIN {
        lo = 0.16 * scale; hi = 0.84 * scale;       # inside the frame-edge clamp
        if (pat == "edges") { lo = 0.10 * scale; hi = 0.999 * scale }
        period = 60;                                 # frames per pingpong cycle
        if (mode == "table")
            printf "%-6s %-7s %-9s %-7s %s\n", "frame", "value", "fraction", "crop_x", "window";
        for (i = 0; i < n; i++) {
            t = i / n;
            if (pat == "pingpong")   { p = frac(i / period + 0.25); c = 1 - abs(1 - 2*p) }
            else if (pat == "sweep") { c = t }
            else if (pat == "hold")  { c = 0.5 }
            else                     { c = (frac(t * 3) < 0.5) ? 0 : 1 }   # edges: slam between extremes
            v = int(lo + (hi - lo) * c + 0.5);
            if (mode == "bytes") {
                printf "\\x%02x\\x%02x\\x%02x\\x%02x", v%256, int(v/256)%256, int(v/65536)%256, int(v/16777216)%256;
            } else {
                # vertical_data_crop_x(): centre = v * scaled_width / SCALE, then clamp
                x = int(v * w / scale) - int(cw/2);
                if (x < 0) x = 0;
                if (x > w - cw) x = w - cw;
                printf "%-6d %-7d %-9.4f %-7d [%d, %d)\n", i, v, v/scale, x, x, x+cw;
            }
        }
    }'
}

# shellcheck disable=SC2059  # the string is \xNN escapes only, by construction
printf "$(pattern_awk bytes)" > "$OUT"

DURATION_TS=$(awk -v n="$FRAMES" -v fps="$FPS" -v tb="$TIMEBASE" 'BEGIN { printf "%d", n * tb / fps }')
BYTES=$(wc -c < "$OUT" | tr -d ' ')

echo "pattern:  $PATTERN, $FRAMES frames -> $OUT ($BYTES bytes)"
echo "crop:     ${CROP_W}x${HEIGHT} window (9:16 of the encoder height)"
echo "duration: $DURATION_TS ts ($FRAMES frames at $FPS fps, timebase 1/$TIMEBASE)"
echo
echo "exc -f $SOURCE -xc-type video -format mp4 -e libx264 \\"
echo "    -enc-height $HEIGHT -video-seg-duration-ts 90000 -duration-ts $DURATION_TS \\"
echo "    -vertical 1 -vertical-data $OUT"

if [ "$TABLE" -eq 1 ]; then
    echo
    pattern_awk table
fi
