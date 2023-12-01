# Create 30 sec mezzanines
./bin/exc -f media/bbb_sunflower_1080p_30fps_normal.mp4 -xc-type video -format fmp4-segment -force-keyint 60 -seg-duration 30

# Text watermarking
./bin/exc -seg-duration 30 -xc-type video -f media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov -wm-text "TEST WATERMARK" -wm-color black -wm-relative-size 0.05 -wm-xloc W/2 -wm-yloc H*0.9 -format fmp4-segment
./bin/exc -seg-duration 30 -xc-type video -f media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov -wm-text "%{pts\\:gmtime\\:1602968400\\:%d-%m-%Y %T}" -wm-color black -wm-relative-size 0.05 -wm-xloc W/2 -wm-yloc H*0.9 -format fmp4-segment
./bin/exc -seg-duration 30 -xc-type video -f media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov -wm-text "%{localtime}" -wm-color black -wm-relative-size 0.05 -wm-xloc W/2 -wm-yloc H*0.9 -format fmp4-segment

# Timecode watermarking
./bin/elvxc transcode --seg-duration 30 --xc-type video -f media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov --wm-timecode-rate 24 --wm-color black --wm-relative-size 0.05 --wm-xloc W/2 --wm-yloc H*0.9 --format fmp4-segment --wm-timecode "00\:00\:00\:00"

# Image watermarking
./bin/elvxc transcode --wm-overlay media/avpipe.png --wm-overlay-type png --wm-xloc "main_w/2-overlay_w/2" --wm-yloc main_h*0.7 -f media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov --xc-type video --seg-duration 30 --format fmp4-segment

# Make HDR mezzanines
./bin/exc -f ./media/SIN5_4K_MOS_J2K_60s_CCBYblendercloud.mxf -format fmp4-segment -seg-duration 30 -d jpeg2000 -e libx265 -force-keyint 48 -xc-type video -max-cll "1514,172" -master-display "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(40000000,50)"

./bin/exc -f ./media/SIN5_4K_MOS_J2K_60s_CCBYblendercloud.mxf -format fmp4-segment -seg-duration 30 -d jpeg2000 -e libx265 -force-keyint 48 -xc-type video -bitdepth 10

# Muxing audio/video
./bin/exc -command mux -mux-spec scripts/sample_mux_spec_abr.txt -f muxed.mp4

# Listen in RTMP mode and make mezzanines
./bin/exc -f rtmp://localhost:5000/test001 -xc-type all -format fmp4-segment -video-seg-duration-ts 480480 -audio-seg-duration-ts 1441440 -force-keyint 4 -listen 1


# Audio join
./bin/elvxc transcode -f ./media/gabby_shading_2mono_1080p.mp4 --xc-type audio-join --audio-index 0,1 --format fmp4-segment --seg-duration 30

ffmpeg-4.2.2-amd64-static/ffmpeg -y -i ./media/gabby_shading_2mono_1080p.mp4 -vn -filter_complex "[0:0][0:1]join=inputs=2:channel_layout=stereo[aout]" -map [aout] -acodec aac -b:a 128k 'out-audio.mp4'


# Audio pan
./bin/exc -f ./media/case_3_video_and_10_channel_audio_10sec.mov -xc-type audio-pan -filter-descriptor "[0:0]pan=5.1|c0=c3|c1=c4|c2=c5|c3=c6|c4=c7|c5=c8[aout]" -format fmp4-segment -seg-duration 30 -channel-layout 5.1

ffmpeg -i ./media/case_3_video_and_10_channel_audio_10sec.mov -vn  -filter_complex "[0:0]pan=5.1|c0=c3|c1=c4|c2=c5|c3=c6|c4=c7|c5=c8[aout]" -map [aout]  -acodec aac -b:a 128k bb.mp4


# Audio merge (not functional yet)
./bin/exc -f ./media/case_2_video_and_8_mono_audio.mp4 -xc-type audio-merge -filter-descriptor "[0:3][0:4][0:5][0:6][0:7][0:8]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2| c3=c3|c4=c4|c5=c5[aout]" -format fmp4-segment -seg-duration 30 -audio-index 3,4,5,6,7,8 -channel-layout stereo

ffmpeg -y -i ./media/case_2_video_and_8_mono_audio.mp4 -vn -filter_complex "[0:3][0:4][0:5][0:6][0:7][0:8]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2| c3=c3|c4=c4|c5=c5[aout]" -map [aout] -acodec aac -b:a 128k audio-merge.mp4

$FFMPEG_BIN/ffmpeg -listen 1 -i rtmp://192.168.90.202:1935/rtmp/XQjNir3S -c copy -flags +global_header -f segment -segment_time 60 -segment_format_options movflags=+faststart -reset_timestamps 1 test%d.mp4


# Extract frames every 2s (default 10s if interval is not set)
./bin/exc -f ./media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov -format image2 -e mjpeg -xc-type extract-images -extract-image-interval-ts 48000

# Extract specified frames
/bin/exc -f ./media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov -format image2 -e mjpeg -xc-type extract-images -extract-images-ts "72000,240000,432000"

# Extract all frames
./bin/exc -f media/bbb_1080p_30fps_60sec.mp4 -xc-type extract-all-images -format image2 -e mjpeg
