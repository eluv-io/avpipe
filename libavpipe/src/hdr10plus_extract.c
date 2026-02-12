/*
 * HDR10+ metadata extraction from video files
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/frame.h>
#include <libavutil/hdr_dynamic_metadata.h>
#include "elv_log.h"
#include "hdr10plus_json.h"

/*
 * Extract HDR10+ metadata from first frame containing dynamic HDR metadata.
 * Returns JSON string that caller must free().
 */
int avpipe_extract_hdr10plus_json(const char *url, char **out_json, int *out_json_len)
{
    if (!url || !out_json || !out_json_len) {
        elv_err("HDR10+ extract: NULL parameters");
        return -1;
    }

    *out_json = NULL;
    *out_json_len = 0;

    AVFormatContext *fmt_ctx = NULL;
    AVCodecContext *codec_ctx = NULL;
    AVFrame *frame = NULL;
    AVPacket *pkt = NULL;
    int ret = -1;
    int video_stream_idx = -1;

    // Open input file
    if (avformat_open_input(&fmt_ctx, url, NULL, NULL) < 0) {
        elv_err("HDR10+ extract: Failed to open input file: %s", url);
        return -1;
    }

    // Find stream info
    if (avformat_find_stream_info(fmt_ctx, NULL) < 0) {
        elv_err("HDR10+ extract: Failed to find stream info");
        goto cleanup;
    }

    // Find video stream
    video_stream_idx = av_find_best_stream(fmt_ctx, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (video_stream_idx < 0) {
        elv_err("HDR10+ extract: No video stream found");
        goto cleanup;
    }

    AVStream *video_stream = fmt_ctx->streams[video_stream_idx];

    // Find decoder
    const AVCodec *codec = avcodec_find_decoder(video_stream->codecpar->codec_id);
    if (!codec) {
        elv_err("HDR10+ extract: Failed to find decoder for codec ID %d",
                video_stream->codecpar->codec_id);
        goto cleanup;
    }

    // Allocate codec context
    codec_ctx = avcodec_alloc_context3(codec);
    if (!codec_ctx) {
        elv_err("HDR10+ extract: Failed to allocate codec context");
        goto cleanup;
    }

    // Copy codec parameters to context
    if (avcodec_parameters_to_context(codec_ctx, video_stream->codecpar) < 0) {
        elv_err("HDR10+ extract: Failed to copy codec parameters");
        goto cleanup;
    }

    // Open codec
    if (avcodec_open2(codec_ctx, codec, NULL) < 0) {
        elv_err("HDR10+ extract: Failed to open codec");
        goto cleanup;
    }

    // Allocate frame and packet
    frame = av_frame_alloc();
    pkt = av_packet_alloc();
    if (!frame || !pkt) {
        elv_err("HDR10+ extract: Failed to allocate frame or packet");
        goto cleanup;
    }

    elv_dbg("HDR10+ extract: Starting to decode frames from %s", url);

    int frames_decoded = 0;
    int max_frames = 100; // Limit search to first 100 frames

    // Read and decode frames until we find HDR10+ metadata
    while (av_read_frame(fmt_ctx, pkt) >= 0 && frames_decoded < max_frames) {
        if (pkt->stream_index == video_stream_idx) {
            // Send packet to decoder
            int send_ret = avcodec_send_packet(codec_ctx, pkt);
            if (send_ret < 0) {
                elv_err("HDR10+ extract: Error sending packet to decoder");
                av_packet_unref(pkt);
                continue;
            }

            // Receive decoded frame
            while (avcodec_receive_frame(codec_ctx, frame) >= 0) {
                frames_decoded++;

                // Check for HDR10+ side data
                AVFrameSideData *sd = av_frame_get_side_data(frame, AV_FRAME_DATA_DYNAMIC_HDR_PLUS);
                if (sd && sd->data && sd->size > 0) {
                    elv_log("HDR10+ extract: Found HDR10+ metadata in frame %d (PTS=%lld, size=%d bytes)",
                            frames_decoded, (long long)frame->pts, sd->size);

                    // Parse the binary HDR10+ data
                    AVDynamicHDRPlus *hdr_meta = (AVDynamicHDRPlus *)sd->data;

                    // Convert to JSON
                    char *json = avpipe_hdr10plus_metadata_to_json(hdr_meta);
                    if (json) {
                        *out_json = json;
                        *out_json_len = strlen(json);
                        elv_log("HDR10+ extract: Successfully extracted %d bytes of JSON", *out_json_len);
                        ret = 0;
                        av_frame_unref(frame);
                        av_packet_unref(pkt);
                        goto cleanup;
                    } else {
                        elv_err("HDR10+ extract: Failed to convert metadata to JSON");
                    }
                }

                av_frame_unref(frame);
            }
        }
        av_packet_unref(pkt);
    }

    // Flush decoder
    avcodec_send_packet(codec_ctx, NULL);
    while (avcodec_receive_frame(codec_ctx, frame) >= 0) {
        frames_decoded++;

        AVFrameSideData *sd = av_frame_get_side_data(frame, AV_FRAME_DATA_DYNAMIC_HDR_PLUS);
        if (sd && sd->data && sd->size > 0) {
            elv_log("HDR10+ extract: Found HDR10+ metadata in flushed frame %d", frames_decoded);

            AVDynamicHDRPlus *hdr_meta = (AVDynamicHDRPlus *)sd->data;
            char *json = avpipe_hdr10plus_metadata_to_json(hdr_meta);
            if (json) {
                *out_json = json;
                *out_json_len = strlen(json);
                elv_log("HDR10+ extract: Successfully extracted %d bytes of JSON", *out_json_len);
                ret = 0;
                av_frame_unref(frame);
                goto cleanup;
            }
        }
        av_frame_unref(frame);
    }

    if (ret != 0) {
        elv_log("HDR10+ extract: No HDR10+ metadata found in %d decoded frames", frames_decoded);
    }

cleanup:
    if (pkt) av_packet_free(&pkt);
    if (frame) av_frame_free(&frame);
    if (codec_ctx) avcodec_free_context(&codec_ctx);
    if (fmt_ctx) avformat_close_input(&fmt_ctx);

    return ret;
}
