./bin/etx -seg-duration 30 -tx-type video -f media/creed_1_min.mov -wm-text "TEST WATERMARK" -wm-color black -wm-relative-size 0.05 -wm-xloc W/2 -wm-yloc H*0.9 -format fmp4-segment

./bin/etx -wm-overlay ./fox_watermark.png -wm-xloc "main_w/2-overlay_w/2" -wm-yloc main_h*0.7 -f O/O1-mez/fsegment0-00001.mp4 -seg-duration-ts 48000 -tx-type video

./bin/avcmd transcode --wm-overlay ./fox_watermark.png --wm-overlay-type png --wm-xloc "main_w/2-overlay_w/2" --wm-yloc main_h*0.7 -f O/O1-mez/fsegment0-00001.mp4 --seg-duration-ts 48000 --tx-type video
