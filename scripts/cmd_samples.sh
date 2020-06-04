./bin/etx -seg-duration 30 -tx-type video -f media/creed_1_min.mov -wm-text "TEST WATERMARK" -wm-color black -wm-relative-size 0.05 -wm-xloc W/2 -wm-yloc H*0.9 -format fmp4-segment

./bin/etx -wm-overlay ./fox_watermark.png -wm-xloc "main_w/2-overlay_w/2" -wm-yloc main_h*0.7 -f O/O1-mez/fsegment0-00001.mp4 -seg-duration-ts 48000 -tx-type video

./bin/avcmd transcode --wm-overlay ./fox_watermark.png --wm-overlay-type png --wm-xloc "main_w/2-overlay_w/2" --wm-yloc main_h*0.7 -f O/O1-mez/fsegment0-00001.mp4 --seg-duration-ts 48000 --tx-type video

./bin/etx -seg-duration 30.03 -f across_the_universe_2160p_h265_veryslow_segmented_60_sec.mp4 -e libx265 -tx-type video -format fmp4-segment -max-cll "1514,172" -master-display "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(40000000,50)"
