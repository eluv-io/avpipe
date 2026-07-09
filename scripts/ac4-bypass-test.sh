#!/usr/bin/env bash
#
# AC-4 bypass (passthrough) C-layer test for avpipe.
#
# Exercises the avpipe C bypass path end-to-end via the `exc` CLI: decoder gate
# (avpipe_xc.c prepare_decoder) -> encoder-less audio bypass setup
# (prepare_audio_encoder) -> do_bypass packet copy -> the FFmpeg fork's `dac4`
# writer. AC-4 has neither an FFmpeg decoder nor encoder, so this is a stronger
# check than the fork's own `ffmpeg -c:a copy` test (tests/elv/ac4-remux-test.sh):
# it proves avpipe can actually *drive* a bypass to an AC-4 mux.
#
# What it verifies:
#   - `exc -bypass 1 -xc-type audio` on an AC-4-in-MP4 source produces, for both
#     `fmp4-segment` and `dash`, an output whose `dac4` config box is
#     byte-identical to the source.
#   - EAC3/Atmos bypass still emits `dec3` (regression guard for the shared
#     audio bypass path).
#
# Requires: a freshly built `bin/exc` linked against the dac4-capable FFmpeg fork
# (build with PKG_CONFIG_PATH pointing at that install, e.g. ~/.local/lib/pkgconfig;
# the on-disk exc may be stale). python3 (for the dac4 byte-compare).
#
# All output is written under $TMPDIR (exc hard-codes a relative ./O dir, so each
# run happens inside a mktemp work dir) and removed on exit unless --keep is given.
#
# Exit codes: 0 = pass, 1 = fail, 77 = skipped (prerequisites absent).
#
# Options:
#   -k, --keep   keep the temp work dir (outputs) instead of deleting on exit
#   -h, --help   show usage
#
# Overridable via env:
#   EXC          path to exc      (default: <repo>/bin/exc)
#   MEDIA_DIR    sample dir       (default: <repo>/media)
#   AC4_SAMPLE   AC-4 file        (default: $MEDIA_DIR/Audio_ID_6ch_128kbps_25fps_ac4.mp4)
#   EC3_SAMPLE   EAC3 file        (default: $MEDIA_DIR/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4)

set -u

# --- options -----------------------------------------------------------------
keep=0
while [ $# -gt 0 ]; do
    case "$1" in
        -k|--keep) keep=1 ;;
        -h|--help)
            sed -n '2,/^set -u/p' "$0" | sed 's/^# \{0,1\}//; /^set -u/d'
            exit 0 ;;
        *) echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
    esac
    shift
done

# --- locate exc + samples ----------------------------------------------------
script_dir=$(cd "$(dirname "$0")" && pwd)
repo_dir=$(cd "$script_dir/.." && pwd)
EXC=${EXC:-$repo_dir/bin/exc}
MEDIA_DIR=${MEDIA_DIR:-$repo_dir/media}
AC4_SAMPLE=${AC4_SAMPLE:-$MEDIA_DIR/Audio_ID_6ch_128kbps_25fps_ac4.mp4}
EC3_SAMPLE=${EC3_SAMPLE:-$MEDIA_DIR/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4}

[ -x "$EXC" ]        || { echo "SKIP: exc not found/executable at $EXC (build it first)"; exit 77; }
[ -f "$AC4_SAMPLE" ] || { echo "SKIP: AC-4 sample not found at $AC4_SAMPLE"; exit 77; }
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 required for dac4 byte-compare"; exit 77; }

# exc bakes an rpath to its FFmpeg libs; keep an optional fallback if the caller
# built against a non-default prefix.
[ -n "${FFMPEG_DIST:-}" ] && export DYLD_LIBRARY_PATH="${FFMPEG_DIST%/}/lib:${DYLD_LIBRARY_PATH:-}"

work=$(mktemp -d "${TMPDIR:-/tmp}/ac4-bypass.XXXXXX")
if [ "$keep" -eq 1 ]; then
    trap 'echo; echo "outputs kept in: $work"' EXIT
else
    trap 'rm -rf "$work"' EXIT
fi

pass=0 fail=0
ok()  { echo "  ok   - $1"; pass=$((pass+1)); }
bad() { echo "  FAIL - $1"; [ -n "${2:-}" ] && sed 's/^/         /' "$2"; fail=$((fail+1)); }

# Extract a box's payload (bytes after the 8-byte box header) from two files and
# compare. Prints MATCH / DIFFER / MISSING. (Same approach as the fork test.)
box_cmp() {
python3 - "$1" "$2" "$3" <<'PY'
import sys
box = sys.argv[3].encode()
def payload(path):
    d = open(path, "rb").read()
    i = d.find(box)
    if i < 4: return None
    size = int.from_bytes(d[i-4:i], "big")   # box size incl. 8-byte header
    return d[i+4 : i-4+size]                  # payload only
a, b = payload(sys.argv[1]), payload(sys.argv[2])
print("MISSING" if a is None or b is None else ("MATCH" if a == b else "DIFFER"))
PY
}

# Run exc bypass in its own subdir (exc writes to ./O/O<n>) and echo that subdir.
run_bypass() {
    local label="$1" fmt="$2" src="$3"
    local d="$work/$label"
    mkdir -p "$d"
    ( cd "$d" && "$EXC" -f "$src" -xc-type audio -bypass 1 -format "$fmt" \
        -seg-duration 30 >exc.log 2>&1 )
    echo "$d"
}

echo "exc:     $EXC"
echo "sample:  $AC4_SAMPLE"
echo

# --- 1) AC-4 bypass: dac4 must survive, byte-identical, on both mux paths -----
echo "[AC-4 bypass] dac4 must survive byte-identical:"

d=$(run_bypass seg fmp4-segment "$AC4_SAMPLE")
seg=$(find "$d/O" -name 'afsegment*.mp4' 2>/dev/null | sort | head -1)
if [ -z "$seg" ]; then bad "fmp4-segment (no output segment)" "$d/exc.log"
else
    case "$(box_cmp "$AC4_SAMPLE" "$seg" dac4)" in
        MATCH)  ok "fmp4-segment (dac4 byte-identical to source)";;
        r)      bad "fmp4-segment (dac4 $r)";;
    esac
fi

d=$(run_bypass dash dash "$AC4_SAMPLE")
init=$(find "$d/O" -name 'ainit-stream*.m4s' 2>/dev/null | head -1)
if [ -z "$init" ]; then bad "dash (no audio init segment)"
else
    case "$(box_cmp "$AC4_SAMPLE" "$init" dac4)" in
        MATCH)  ok "dash init (dac4 byte-identical to source)";;
        r)      bad "dash init (dac4 $r)";;
    esac
fi

# --- 2) AC-4 probe: prepare_decoder must identify AC-4 despite having no decoder --
echo
echo "[AC-4 probe] must identify AC-4 (no decoder required):"
d="$work/probe"; mkdir -p "$d"
( cd "$d" && "$EXC" -command probe -f "$AC4_SAMPLE" >probe.out 2>probe.err )
if grep -qi 'codec_name:[[:space:]]*ac4' "$d/probe.out"; then ok "probe reports codec_name ac4"
else bad "probe did not identify AC-4" "$d/probe.out"; fi

# --- 3) EAC3 regression: the shared audio bypass path must still emit dec3 ----
if [ -f "$EC3_SAMPLE" ]; then
    echo
    echo "[regression] EAC3 bypass dec3 still present:"
    d=$(run_bypass ec3 fmp4-segment "$EC3_SAMPLE")
    seg=$(find "$d/O" -name 'afsegment*.mp4' 2>/dev/null | sort | head -1)
    if [ -z "$seg" ]; then bad "EAC3 bypass (no output segment)"
    elif grep -qa 'dec3' "$seg"; then ok "EAC3 dec3 present"
    else bad "EAC3 dec3 missing"; fi
else
    echo
    echo "  (skip EAC3 regression: $EC3_SAMPLE not found)"
fi

echo
echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
