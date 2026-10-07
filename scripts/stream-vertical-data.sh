#!/usr/bin/env bash
#
# Transcode a clip with vertical crop data streamed through a named FIFO
#
# Usage: stream-vertical-data.sh -f media -d crop.bin [options] [-- elvxc args]
#   -f file      input media (required)
#   -d file      crop data, one little-endian uint32 per frame (required);
#                make one with gen-vertical-data.sh
#   -o dir       work directory (default: the current directory); the output
#                lands in <dir>/O/O<handle>/, elvxc.log beside it
#   -H height    encoder height (default 720); the crop is 9:16 of it
#   -D ts        duration in input timebase units (default -1: whole input).
#                Once the data runs out the crop holds its last value.
#   -S ts        video segment duration (default: a single segment)
#   -r rate      records per second fed to the FIFO (default 30, about live;
#                0 writes them all at once and elvxc runs flat out)
#   ELVXC=path   elvxc binary (default <repo>/elvxc/elvxc; build it with
#                "go build -o elvxc/elvxc ./elvxc")
# Anything after -- is appended to the elvxc transcode command line.
#
# Example - 10 s of bbb with a 300-frame pingpong pan:
#   scripts/gen-vertical-data.sh -p pingpong -n 300 -o pingpong.bin
#   scripts/stream-vertical-data.sh -f media/bbb_1080p_30fps_60sec.mp4 -d pingpong.bin -D 300000
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
INPUT= DATA= WORK= HEIGHT=720 DURATION=-1 SEG= RATE=30

usage() { sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while getopts "f:d:o:H:D:S:r:h" opt; do
    case $opt in
        f) INPUT=$OPTARG ;;
        d) DATA=$OPTARG ;;
        o) WORK=$OPTARG ;;
        H) HEIGHT=$OPTARG ;;
        D) DURATION=$OPTARG ;;
        S) SEG=$OPTARG ;;
        r) RATE=$OPTARG ;;
        h) usage 0 ;;
        *) usage 1 ;;
    esac
done
shift $((OPTIND - 1))

[ -n "$INPUT" ] && [ -n "$DATA" ] || usage 1
[ -f "$INPUT" ] || { echo "input not found: $INPUT" >&2; exit 1; }
[ -f "$DATA" ] || { echo "crop data not found: $DATA" >&2; exit 1; }
RECORDS=$(( $(wc -c < "$DATA") / 4 ))
[ "$RECORDS" -gt 0 ] || { echo "crop data holds no records: $DATA" >&2; exit 1; }

ELVXC=${ELVXC:-$ROOT/elvxc/elvxc}
[ -x "$ELVXC" ] || {
    echo "elvxc not found at $ELVXC - build it: (cd $ROOT && go build -o elvxc/elvxc ./elvxc)" >&2
    exit 1
}

# A single segment unless asked otherwise: the whole duration when it is known,
# otherwise a value no input will reach.
if [ -z "$SEG" ]; then
    if [ "$DURATION" -gt 0 ]; then SEG=$DURATION; else SEG=36000000000; fi
fi
WIDTH=$(( HEIGHT * 16 / 9 )); WIDTH=$(( WIDTH - WIDTH % 2 ))

# elvxc runs inside the work directory (it writes O/ relative to its cwd), so
# the paths it is given must be absolute.
INPUT=$(cd "$(dirname "$INPUT")" && pwd)/$(basename "$INPUT")
DATA=$(cd "$(dirname "$DATA")" && pwd)/$(basename "$DATA")
WORK=${WORK:-.}
mkdir -p "$WORK"
WORK=$(cd "$WORK" && pwd)
FIFO=$WORK/crop.fifo
rm -f "$FIFO"
mkfifo "$FIFO"

PRODUCER=""
cleanup() {
    # The producer is still blocked opening the FIFO if elvxc never opened it.
    [ -z "$PRODUCER" ] || kill "$PRODUCER" 2> /dev/null || true
    rm -f "$FIFO"
}
trap cleanup EXIT

# The producer. Opening the FIFO for writing blocks until elvxc opens it for
# reading; once elvxc has closed its end the next write fails and the loop ends.
feed() {
    trap '' PIPE
    exec 3> "$FIFO"
    if [ "$RATE" = 0 ]; then
        cat "$DATA" >&3
    else
        local i delay
        delay=$(awk -v r="$RATE" 'BEGIN { printf "%.4f", 1 / r }')
        for ((i = 0; i < RECORDS; i++)); do
            dd if="$DATA" bs=4 count=1 skip="$i" 2> /dev/null >&3 || break
            sleep "$delay"
        done
    fi
    exec 3>&-
}
feed &
PRODUCER=$!

echo "streaming $RECORDS records from $DATA at ${RATE}/s through $FIFO"
echo "work dir: $WORK"
(cd "$WORK" && "$ELVXC" transcode -f "$INPUT" \
    --format fmp4-segment --xc-type video \
    --duration-ts "$DURATION" --video-seg-duration-ts "$SEG" \
    --enc-height "$HEIGHT" --enc-width "$WIDTH" \
    --vertical 1 --vertical-data "$FIFO" --threads 1 "$@")
kill "$PRODUCER" 2> /dev/null || true
wait "$PRODUCER" 2> /dev/null || true
PRODUCER=""

SEGMENTS=$(find "$WORK/O" -name 'fmp4-vsegment*' | sort)
[ -n "$SEGMENTS" ] || { echo "no output segments under $WORK/O" >&2; exit 1; }
echo "output:"
echo "$SEGMENTS" | sed 's/^/  /'
if command -v ffprobe > /dev/null; then
    FIRST=$(echo "$SEGMENTS" | head -1)
    echo "size: $(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 "$FIRST")"
fi
