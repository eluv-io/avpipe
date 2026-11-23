/*
 * A complete caller of the SmartFeed Augmentor library, in C, end to end.
 *
 * It makes up a frame, a mask, a metadata document and an advert image, augments the frame
 * once, and reports what happened. Nothing here comes from a real stream, so it proves nothing
 * about a real event - what it does prove is that the library loads on this machine, that its
 * GPU can run the kernels the .so was built for, and that the calling sequence is right.
 *
 * Run it after building, before wiring anything into a pipeline:
 *
 *     ./sfa_augment_one_frame
 *
 * Exit status is 0 when the frame came back augmented.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "smartfeed_augmentor.h"

/* The frame the host decodes. */
#define FRAME_WIDTH 1920
#define FRAME_HEIGHT 1080

/* The space the producer works in, and the space the corners and the mask below are expressed
 * in. It need not match the frame: the library scales between them, so a 720p producer feeding
 * a 1080p stream is an ordinary case rather than a special one. */
#define PROCESSING_WIDTH 1280
#define PROCESSING_HEIGHT 720

/* The per-frame document a producer writes. processingWidth and processingHeight are the
 * coordinate space the corners below are expressed in, and are required. */
static const char* kMetadataJson =
    "{"
    "  \"timecode\": \"01:00:00:00\","
    "  \"processingWidth\": 1280,"
    "  \"processingHeight\": 720,"
    "  \"isLive\": true,"
    "  \"period\": \"1\","
    "  \"placeholders\": {"
    "    \"group-a\": {"
    "      \"name\": \"CourtSide\","
    "      \"corners\": ["
    "        {\"x\": 400, \"y\": 300},"
    "        {\"x\": 880, \"y\": 300},"
    "        {\"x\": 880, \"y\": 480},"
    "        {\"x\": 400, \"y\": 480}"
    "      ]"
    "    }"
    "  }"
    "}";

static void on_log(int level, const char* message, void* user)
{
    (void)level;
    (void)user;
    fprintf(stderr, "[augmentor] %s\n", message);
}

int main(void)
{
    sfa_init_params params;
    sfa_creative creative;
    sfa_frame_metadata metadata;
    sfa_image frame;
    sfa_node* node = NULL;
    sfa_status status;

    unsigned char* luma = NULL;
    unsigned char* chroma_u = NULL;
    unsigned char* chroma_v = NULL;
    unsigned char* mask = NULL;
    unsigned char* advert = NULL;
    unsigned char* before = NULL;
    size_t luma_bytes = (size_t)FRAME_WIDTH * FRAME_HEIGHT;
    size_t chroma_bytes = luma_bytes / 4;
    size_t mask_bytes = (size_t)PROCESSING_WIDTH * PROCESSING_HEIGHT;
    size_t changed = 0;
    size_t i;
    int exit_code = 1;

    /* Before anything else: the structs below are passed by value, so a library from
     * another version reads them at the wrong offsets rather than refusing them. */
    if (sfa_abi_version() != SFA_ABI_VERSION)
    {
        fprintf(stderr, "built against ABI %u, linked %u\n", (unsigned)SFA_ABI_VERSION,
                sfa_abi_version());
        return 1;
    }
    printf("%s\n", sfa_build_info());

    /* A mid-grey frame. I420: full resolution luma, quarter resolution chroma. */
    luma = (unsigned char*)malloc(luma_bytes);
    chroma_u = (unsigned char*)malloc(chroma_bytes);
    chroma_v = (unsigned char*)malloc(chroma_bytes);
    before = (unsigned char*)malloc(luma_bytes);
    /* Processing-sized, NOT frame-sized: the mask is indexed in the same space as the corners,
     * and one of any other size is refused rather than stretched. White draws, black does not,
     * so an all-white mask draws the whole placement. */
    mask = (unsigned char*)malloc(mask_bytes);
    /* A solid blue advert, BGRA with straight alpha. */
    advert = (unsigned char*)malloc((size_t)256 * 128 * 4);

    if (!luma || !chroma_u || !chroma_v || !before || !mask || !advert)
    {
        fprintf(stderr, "out of memory\n");
        goto done;
    }

    memset(luma, 128, luma_bytes);
    memset(chroma_u, 128, chroma_bytes);
    memset(chroma_v, 128, chroma_bytes);
    memset(mask, 255, mask_bytes);
    memcpy(before, luma, luma_bytes);

    for (i = 0; i < (size_t)256 * 128; ++i)
    {
        advert[i * 4 + 0] = 220; /* B */
        advert[i * 4 + 1] = 90;  /* G */
        advert[i * 4 + 2] = 30;  /* R */
        advert[i * 4 + 3] = 255; /* A */
    }

    memset(&creative, 0, sizeof(creative));
    /* Matched against the placement named in the document, most specific first:
     * "CourtSide_1", then "CourtSide", then "*". */
    creative.name = "CourtSide";
    creative.bgra = advert;
    creative.stride = 256 * 4;
    creative.width = 256;
    creative.height = 128;

    memset(&params, 0, sizeof(params));
    params.cuda_device = -1; /* the current device */
    params.fps = 59.94005994005994;
    params.creatives = &creative;
    params.n_creatives = 1;
    params.log_cb = on_log;

    status = sfa_init(&params, &node);
    if (status != SFA_OK)
    {
        fprintf(stderr, "sfa_init failed: %d\n", (int)status);
        goto done;
    }

    memset(&frame, 0, sizeof(frame));
    frame.width = FRAME_WIDTH;
    frame.height = FRAME_HEIGHT;
    frame.plane[0] = luma;
    frame.plane[1] = chroma_u;
    frame.plane[2] = chroma_v;
    frame.stride[0] = FRAME_WIDTH;
    frame.stride[1] = FRAME_WIDTH / 2;
    frame.stride[2] = FRAME_WIDTH / 2;

    memset(&metadata, 0, sizeof(metadata));
    metadata.json = kMetadataJson;
    metadata.json_len = (int32_t)strlen(kMetadataJson);
    metadata.mask = mask;
    metadata.mask_stride = PROCESSING_WIDTH;
    metadata.mask_width = PROCESSING_WIDTH;
    metadata.mask_height = PROCESSING_HEIGHT;

    status = sfa_augment(node, &frame, &metadata);

    switch (status)
    {
    case SFA_OK:
        for (i = 0; i < luma_bytes; ++i)
        {
            if (luma[i] != before[i])
            {
                ++changed;
            }
        }
        printf("SFA_OK: %lu of %lu luma samples changed\n", (unsigned long)changed,
               (unsigned long)luma_bytes);
        if (changed == 0)
        {
            fprintf(stderr, "reported OK but drew nothing\n");
            goto destroy;
        }
        exit_code = 0;
        break;

    case SFA_UNMODIFIED:
        /* The frame is untouched and the caller forwards its own. Here it means nothing was
         * drawn, which for this document means something is wrong. */
        fprintf(stderr, "SFA_UNMODIFIED: nothing was drawn (%s)\n", sfa_last_error(node));
        break;

    case SFA_FRAME_DAMAGED:
        /* Discard the frame: it is neither the source nor a correctly augmented one. */
        fprintf(stderr, "SFA_FRAME_DAMAGED: %s\n", sfa_last_error(node));
        break;

    default:
        fprintf(stderr, "sfa_augment failed (%d): %s\n", (int)status, sfa_last_error(node));
        break;
    }

destroy:
    sfa_destroy(node);

done:
    free(luma);
    free(chroma_u);
    free(chroma_v);
    free(before);
    free(mask);
    free(advert);
    return exit_code;
}
