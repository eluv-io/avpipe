#!/bin/bash

if [ -z $FFMPEG_BIN ]
then
    echo "Set FFMPEG_BIN env variable"
    exit 1
fi

function usage {
    echo "Usage: fff.sh [--dump-streams | --dump-streams-json | --dump-frames <stream#> | --dump-box | --dump-tracks | --dump-media | --dump-headers]  <filename>"
    echo "--dump-streams <filename>          -> dumps information about the streams in <filename> using ffprobe"
    echo "--dump-streams-json <filename>     -> dumps information in json format about the streams in <filename> using ffprobe"
    echo "--dump-frames <stream#> <filename> -> dumps every frams of stream <stream#> in <filename> using ffprobe"
    echo "--dump-box <filename>              -> dumps mp4 boxes in <filename> using mp4box"
    echo "--dump-tracks <filename>           -> dumps tracks in <filename> using mp4info"
    echo "--dump-media <filename>            -> dumps streams info in <filename> using mediainfo"
    echo "--dump-headers <filename>          -> dumps mp4 headers in <filename> using ffmpeg"
    echo "To use this script ffmpeg, Bento4 (mp4dump, mp4info), and mediainfo command line utilities must be installed"
    exit 1
}

function check_filename {
    if [ $# -eq 0 ]
    then
        echo "Error: missing <filename>"
        usage
    fi
}

function check_stream_filename {
    if [ ! $# -eq 2 ]
    then
        echo "Error: missing <stream#> or <filename>"
        usage
    fi
}

if [ $# -eq 0 ]
then
    usage
fi

while [[ $# -gt 0 ]]
do
arg="$1"
case $arg in
    -h|--help)
    usage
    shift
    ;;

    --dump-headers)
    check_filename $2
    $FFMPEG_BIN/ffmpeg -i $2 -c copy -bsf:v trace_headers -f null -
    shift
    shift
    ;;

    --dump-streams)
    check_filename $2
    $FFMPEG_BIN/ffprobe -i $2 -show_streams
    shift
    shift
    ;;

    --dump-streams-json)
    check_filename $2
    $FFMPEG_BIN/ffprobe -v quiet -print_format json -show_format -show_streams $2
    shift
    shift
    ;;

    --dump-frames)
    check_stream_filename $2 $3
    $FFMPEG_BIN/ffprobe -hide_banner -show_frames -select_streams $2 -print_format json $3
    shift
    shift
    ;;

    --dump-box)
    check_filename $2
    mp4dump $2
    shift
    shift
    ;;

    --dump-tracks)
    check_filename $2
    mp4info $2
    shift
    shift
    ;;

    --dump-media)
    check_filename $2
    mediainfo $2
    shift
    shift
    ;;

    *)
    echo "Error: invalid argument" $arg
    usage
esac
done

