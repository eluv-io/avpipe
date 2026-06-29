/*
 * avpipe_codec_refs.c
 *
 * Codec reference-frame helpers.
 */

#include <stdint.h>
#include <string.h>

#include "avpipe_codec_refs.h"
#include "elv_log.h"

typedef struct bit_reader_t {
    const uint8_t *data;
    int size;
    int bit_pos;
} bit_reader_t;

static int
br_bits_left(
    const bit_reader_t *br)
{
    return br->size * 8 - br->bit_pos;
}

static int
br_read_bit(
    bit_reader_t *br,
    unsigned *out)
{
    if (br_bits_left(br) < 1)
        return -1;

    int byte_pos = br->bit_pos / 8;
    int bit_pos = 7 - (br->bit_pos % 8);
    *out = (br->data[byte_pos] >> bit_pos) & 1;
    br->bit_pos++;
    return 0;
}

static int
br_read_bits(
    bit_reader_t *br,
    int n,
    unsigned *out)
{
    unsigned v = 0;

    if (n < 0 || n > 31 || br_bits_left(br) < n)
        return -1;
    for (int i = 0; i < n; i++) {
        unsigned bit = 0;
        if (br_read_bit(br, &bit) < 0)
            return -1;
        v = (v << 1) | bit;
    }
    *out = v;
    return 0;
}

static int
br_skip_bits(
    bit_reader_t *br,
    int n)
{
    unsigned tmp = 0;
    return br_read_bits(br, n, &tmp);
}

static int
br_read_ue(
    bit_reader_t *br,
    unsigned *out)
{
    int zeros = 0;
    unsigned bit = 0;

    while (br_bits_left(br) > 0) {
        if (br_read_bit(br, &bit) < 0)
            return -1;
        if (bit)
            break;
        zeros++;
        if (zeros > 31)
            return -1;
    }
    if (!bit)
        return -1;

    unsigned suffix = 0;
    if (zeros > 0 && br_read_bits(br, zeros, &suffix) < 0)
        return -1;
    *out = ((1u << zeros) - 1u) + suffix;
    return 0;
}

static int
br_skip_ue(
    bit_reader_t *br)
{
    unsigned tmp = 0;
    return br_read_ue(br, &tmp);
}

static int
br_read_se(
    bit_reader_t *br,
    int *out)
{
    unsigned ue = 0;

    if (br_read_ue(br, &ue) < 0)
        return -1;
    *out = (ue & 1) ? (int)((ue + 1) / 2) : -(int)(ue / 2);
    return 0;
}

static int
br_skip_se(
    bit_reader_t *br)
{
    int tmp = 0;
    return br_read_se(br, &tmp);
}

static int
h264_skip_scaling_list(
    bit_reader_t *br,
    int size)
{
    int last_scale = 8;
    int next_scale = 8;

    for (int i = 0; i < size; i++) {
        if (next_scale != 0) {
            int delta_scale = 0;
            if (br_read_se(br, &delta_scale) < 0)
                return -1;
            next_scale = (last_scale + delta_scale + 256) % 256;
        }
        last_scale = next_scale == 0 ? last_scale : next_scale;
    }
    return 0;
}

static int
h264_rbsp_from_ebsp(
    const uint8_t *src,
    int src_size,
    uint8_t *dst)
{
    int dst_size = 0;
    int zeros = 0;

    for (int i = 0; i < src_size; i++) {
        if (zeros == 2 && src[i] == 0x03) {
            zeros = 0;
            continue;
        }

        dst[dst_size++] = src[i];
        if (src[i] == 0)
            zeros++;
        else
            zeros = 0;
    }
    return dst_size;
}

static int
h264_refs_from_sps_nal(
    const uint8_t *sps,
    int sps_size)
{
    uint8_t rbsp[1024];
    bit_reader_t br;
    unsigned profile_idc = 0;
    unsigned pic_order_cnt_type = 0;
    unsigned num_ref_frames = 0;

    if (!sps || sps_size <= 1 || (sps[0] & 0x1f) != 7)
        return 0;

    if (sps_size - 1 > (int) sizeof(rbsp))
        return 0;

    int rbsp_size = h264_rbsp_from_ebsp(sps + 1, sps_size - 1, rbsp);
    br = (bit_reader_t){ .data = rbsp, .size = rbsp_size, .bit_pos = 0 };

    if (br_read_bits(&br, 8, &profile_idc) < 0 || // profile_idc
        br_skip_bits(&br, 8) < 0 || // constraints/reserved
        br_skip_bits(&br, 8) < 0 || // level_idc
        br_skip_ue(&br) < 0) // seq_parameter_set_id
        return 0;

    switch (profile_idc) {
    case 100: case 110: case 122: case 244: case 44: case 83: case 86:
    case 118: case 128: case 138: case 139: case 134: case 135:
        {
            unsigned chroma_format_idc = 0;
            unsigned scaling_matrix_present = 0;
            if (br_read_ue(&br, &chroma_format_idc) < 0)
                return 0;
            if (chroma_format_idc == 3) {
                unsigned separate_colour_plane_flag = 0;
                if (br_read_bit(&br, &separate_colour_plane_flag) < 0)
                    return 0;
            }
            if (br_skip_ue(&br) < 0 || // bit_depth_luma_minus8
                br_skip_ue(&br) < 0 || // bit_depth_chroma_minus8
                br_skip_bits(&br, 1) < 0 || // qpprime_y_zero_transform_bypass_flag
                br_read_bit(&br, &scaling_matrix_present) < 0)
                return 0;
            if (scaling_matrix_present) {
                int scaling_lists = chroma_format_idc != 3 ? 8 : 12;
                for (int i = 0; i < scaling_lists; i++) {
                    unsigned present = 0;
                    if (br_read_bit(&br, &present) < 0)
                        return 0;
                    if (present && h264_skip_scaling_list(&br, i < 6 ? 16 : 64) < 0)
                        return 0;
                }
            }
        }
        break;
    default:
        break;
    }

    if (br_skip_ue(&br) < 0 || // log2_max_frame_num_minus4
        br_read_ue(&br, &pic_order_cnt_type) < 0)
        return 0;

    if (pic_order_cnt_type == 0) {
        if (br_skip_ue(&br) < 0)
            return 0;
    } else if (pic_order_cnt_type == 1) {
        unsigned cycle = 0;
        if (br_skip_bits(&br, 1) < 0 ||
            br_skip_se(&br) < 0 ||
            br_skip_se(&br) < 0 ||
            br_read_ue(&br, &cycle) < 0)
            return 0;
        for (unsigned i = 0; i < cycle; i++) {
            if (br_skip_se(&br) < 0)
                return 0;
        }
    }

    if (br_read_ue(&br, &num_ref_frames) < 0)
        return 0;
    return (int) num_ref_frames;
}

int
avpipe_h264_refs_from_extradata(
    const uint8_t *extradata,
    int extradata_size)
{
    if (!extradata || extradata_size <= 0)
        return 0;

    if (extradata_size >= 7 && extradata[0] == 1) {
        int off = 6;
        int sps_count = extradata[5] & 0x1f;
        for (int i = 0; i < sps_count && off + 2 <= extradata_size; i++) {
            int sps_size = (extradata[off] << 8) | extradata[off + 1];
            off += 2;
            if (sps_size <= 0 || off + sps_size > extradata_size)
                return 0;
            int refs = h264_refs_from_sps_nal(extradata + off, sps_size);
            if (refs > 0)
                return refs;
            off += sps_size;
        }
        return 0;
    }

    for (int i = 0; i + 5 < extradata_size; i++) {
        int start = -1;
        if (extradata[i] == 0 && extradata[i + 1] == 0 && extradata[i + 2] == 1)
            start = i + 3;
        else if (i + 6 < extradata_size &&
                 extradata[i] == 0 && extradata[i + 1] == 0 &&
                 extradata[i + 2] == 0 && extradata[i + 3] == 1)
            start = i + 4;
        if (start < 0 || (extradata[start] & 0x1f) != 7)
            continue;

        int end = extradata_size;
        for (int j = start + 1; j + 3 < extradata_size; j++) {
            if (extradata[j] == 0 && extradata[j + 1] == 0 &&
                (extradata[j + 2] == 1 || (j + 3 < extradata_size && extradata[j + 2] == 0 && extradata[j + 3] == 1))) {
                end = j;
                break;
            }
        }
        int refs = h264_refs_from_sps_nal(extradata + start, end - start);
        if (refs > 0)
            return refs;
    }

    return 0;
}

int
avpipe_source_h264_refs(
    coderctx_t *decoder_context,
    int index,
    int *sps_refs,
    int *codec_context_refs,
    const char **used)
{
    AVCodecParameters *codecpar = decoder_context->stream[index]->codecpar;
    int refs_from_sps = avpipe_h264_refs_from_extradata(codecpar->extradata, codecpar->extradata_size);
    int refs_from_codec_context = decoder_context->codec_context[index]->refs;

    if (sps_refs)
        *sps_refs = refs_from_sps;
    if (codec_context_refs)
        *codec_context_refs = refs_from_codec_context;

    if (refs_from_sps > 0) {
        if (used)
            *used = "sps";
        return refs_from_sps;
    }
    if (refs_from_codec_context > 0) {
        if (used)
            *used = "codec_context";
        return refs_from_codec_context;
    }

    if (used)
        *used = "none";
    return 0;
}

void
avpipe_apply_h264_refs(
    AVCodecContext *encoder_codec_context,
    coderctx_t *decoder_context,
    int index,
    xcparams_t *params)
{
    if (params->h264_refs > 0 || !strcmp(params->format, "dash")) {
        int sps_refs = 0;
        int codec_context_refs = 0;
        int encoder_refs_before = encoder_codec_context->refs;
        const char *used = "none";
        int refs = avpipe_source_h264_refs(decoder_context, index, &sps_refs, &codec_context_refs, &used);

        if (params->h264_refs > 0) {
            refs = params->h264_refs;
            used = "params";
        }

        if (refs > 0)
            encoder_codec_context->refs = refs;

        elv_log("H264 refs selection used=%s selected_refs=%d params_refs=%d sps_refs=%d codec_context_refs=%d encoder_refs_before=%d encoder_refs=%d format=%s url=%s",
            used,
            refs,
            params->h264_refs,
            sps_refs,
            codec_context_refs,
            encoder_refs_before,
            encoder_codec_context->refs,
            params->format,
            params->url);
    }
}
