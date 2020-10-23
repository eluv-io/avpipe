#!/bin/bash
#
# Source: https://github.com/terabit-software/dynamic-stream-server/blob/master/scripts/sync_test.sh
#
# Tool to check video and audio sync.
# Loop with 1 beep per second with 4 increasing frequencies.
# Can be used to measure delays up to 4 seconds.
# The grid is accurate to about 1ms.
#
# Beep duration: 0.125s
#
# An argument may be passed (e.g. "ffmpeg") to record to a file:
#   ./sync-test.sh hls
#


# Configuration
W="860"
H="300"
FPS="120"
DURATION="0.125"
font="/usr/share/fonts/truetype/freefont/FreeSerif.ttf"

cmd="ffplay"
case ${1} in
ffmpeg)
  cmd="ffmpeg"
  ;;
hls)
  # Reproduce HLS.js live bug: https://github.com/video-dev/hls.js/issues/2545
  cmd="ffmpeg"
  FPS="60000/1001"
  ;;
dazn)
  cmd="ffmpeg"
  FPS="50"
  ;;
esac

# --------------------------------------------------------------

# Display
margin="0.95"
display_volume="0.5"

# Aux
size="$W"x"$H"
T="floor(t)"
beep="lt(mod(t\,1)\,0.125)"
hz="*2*PI*t"

# Sine waves
f_1="(sin( 440 $hz) + sin( 880 $hz)/3 + sin(1320 $hz)/5 + sin(1760 $hz)/7)"
f_2="(sin( 660 $hz) + sin(1320 $hz)/4 + sin(1980 $hz)/6)"
f_3="(sin( 880 $hz) + sin(1760 $hz)/3)"
f_4="(sin(1320 $hz) + sin(2640 $hz)/4)"

FILTER="aevalsrc=\
    if(mod($T\,2)           \,\
        if(mod($T+1\,4)     \,\
            $f_2 * $beep    \,\
            $f_4 * $beep    \
        )\,\
        if(mod($T\,4)       \,\
            $f_3 * $beep    \,\
            $f_1 * $beep    \
        )\
    ) * $margin + .01,      \
    asplit[a][out1];        \
    color=c=red:s=200x60:r=$FPS, hue=H=mod(t\,4)*1.7[c];           \
    [a]volume=$display_volume, showwaves=s=$size:mode=line:r=$FPS, \
    fps=$FPS, drawgrid=c=#334347:w=22:h=299,                                \
    drawtext=fontsize=30:fix_bounds=1:fontcolor=#ffffff:fontfile=$font:y=15:text='%{pts\:hms}'[wave]; \
    [wave][c]overlay=W/2-100:5:enable=lte(mod(t\,1)\,$DURATION)[out0]"

# Running
args=(${cmd} -f lavfi -i "${FILTER}")

case ${1} in
ffmpeg)
  args+=(-t 300 -b:v 10M -c:a libfdk_aac -b:a 256K sync.mp4)
  ;;
hls)
  args+=(-map 0:v -map 0:a -b:v 10M -g 120 -force_key_frames "expr:gte(t,n_forced*2.002)" -c:a libfdk_aac -b:a 256K -f hls -hls_time 2 -hls_segment_type fmp4 -var_stream_map "a:0,name:audio,agroup:audio v:0,name:video,agroup:audio" -hls_segment_filename "%v/%d.fmp4" -master_pl_name master.m3u8 "%v/playlist.m3u8")
  ;;
dazn)
  args+=(-map 0:v -map 0:a -b:v 10M -g 100 -force_key_frames "expr:gte(t,n_forced*2)" -c:a libfdk_aac -b:a 256K -f hls -hls_time 2 -hls_segment_type fmp4 -var_stream_map "a:0,name:audio,agroup:audio v:0,name:video,agroup:audio" -hls_segment_filename "%v/%d.fmp4" -master_pl_name master.m3u8 "%v/playlist.m3u8")
  ;;
esac

echo "${args[@]}"
echo -e "\n\n"
"${args[@]}"
