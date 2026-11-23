#ifndef SMARTFEED_AUGMENTOR_H
#define SMARTFEED_AUGMENTOR_H

/*
 * SmartFeed Augmentor - per-frame advert augmentation for a host video pipeline.
 *
 * The host decodes, pairs each frame with its metadata, and calls sfa_augment once per frame.
 * One node augments one output; create several for several outputs.
 *
 * Typical use:
 *
 *     sfa_node* node;
 *     if (sfa_init(&params, &node) != SFA_OK) { ... }
 *
 *     for each frame:
 *         switch (sfa_augment(node, &frame, &metadata))
 *         {
 *         case SFA_OK:         the frame now carries an advert; break;
 *         case SFA_UNMODIFIED: the frame is unchanged;          break;
 *         default:             log sfa_last_error(node);
 *         }
 *
 *     sfa_destroy(node);
 */

#include <stdint.h>

#if defined(_WIN32)
#  if defined(SFA_BUILDING)
#    define SFA_API __declspec(dllexport)
#  else
#    define SFA_API __declspec(dllimport)
#  endif
#else
#  define SFA_API __attribute__((visibility("default")))
#endif

#ifdef __cplusplus
extern "C" {
#endif

typedef struct sfa_node sfa_node;

typedef enum
{
    /** Success: the frame was augmented in place. */
    SFA_OK = 0,

    /** Nothing was drawn. The frame is byte for byte as it was passed in. */
    SFA_UNMODIFIED = 1,

    /** Configuration or stream properties this node will not accept. */
    SFA_REFUSED = 2,

    /** Invalid argument: a null pointer, a non-positive stride, or a size mismatch. */
    SFA_INVALID = 3,

    /** Internal failure. The frame was not written and remains usable. */
    SFA_INTERNAL = 4,

    /**
     * The renderer failed part way through writing the frame, so it may hold some augmented
     * planes and some original ones. Discard it: it is neither the source frame nor a
     * correctly augmented one.
     */
    SFA_FRAME_DAMAGED = 5
} sfa_status;

/**
 * This header's version, for checking against the library that is linked. See
 * sfa_abi_version(). It changes whenever a struct or a status in this file does.
 */
#define SFA_ABI_VERSION 1

/** Levels passed to the log callback in sfa_init_params. */
#define SFA_LOG_DEBUG 0
#define SFA_LOG_INFO 1
#define SFA_LOG_WARN 2
#define SFA_LOG_ERROR 3

/**
 * A video frame in planar I420 (YUV 4:2:0), in host memory. Augmented in place.
 *
 * plane[0] is Y at full resolution; plane[1] and plane[2] are U and V at half resolution in
 * each dimension. stride[i] is the byte offset between consecutive rows of plane[i] and may
 * exceed the plane width.
 */
typedef struct
{
    uint8_t* plane[3];
    int32_t stride[3];
    int32_t width;
    int32_t height;
} sfa_image;

/**
 * One frame's augmentation metadata, paired with the frame by the host.
 */
typedef struct
{
    /** The per-frame metadata document, UTF-8. Need not be null-terminated. */
    const char* json;
    int32_t json_len;

    /**
     * Optional full-frame mask, 8 bits per pixel, sized to the processing space declared in
     * the document. NULL if the frame has no mask.
     *
     * White draws and black does not: a foreground object that should occlude an advert is
     * black. mask_stride is the byte offset between consecutive rows.
     *
     * A placement with no mask is not drawn, so a frame passed with mask NULL is returned
     * unaugmented.
     */
    const uint8_t* mask;
    int32_t mask_stride;
    int32_t mask_width;
    int32_t mask_height;
} sfa_frame_metadata;

/**
 * One decoded advert image. The library performs no image decoding.
 */
typedef struct
{
    /**
     * The placement this image is drawn on. Matched against the placements named in the
     * per-frame metadata, most specific first: "Name_period", then "Name", then "*".
     */
    const char* name;

    /** BGRA, 8 bits per channel, straight (non-premultiplied) alpha. */
    const uint8_t* bgra;
    int32_t stride;
    int32_t width;
    int32_t height;
} sfa_creative;

typedef struct
{
    /**
     * CUDA device ordinal used for rendering, or -1 for the current device.
     */
    int32_t cuda_device;

    /** Frame rate of the stream, in frames per second. Must be greater than zero. */
    double fps;

    /** Advert images, copied during sfa_init. May be NULL when n_creatives is 0. */
    const sfa_creative* creatives;
    int32_t n_creatives;

    /**
     * Where this library's logs go. If Null, no logs will be produced.
     */
    void (*log_cb)(int level, const char* message, void* user);
    void* log_user;
} sfa_init_params;

/**
 * What this node has done, and why it did not do more.
 * Counts are cumulative since sfa_init. Reading them costs nothing and does not reset them.
 */
typedef struct
{
    /** Calls to sfa_augment, and how each one ended. */
    uint64_t frames_seen;
    uint64_t frames_augmented;
    uint64_t frames_unmodified;
    uint64_t frames_failed;

    /**
     * Why a placement was not drawn. "No advert appeared" has several causes that look
     * identical from outside, and these are what tell them apart.
     *
     * skipped_no_creative is the one to read first on a new integration: it counts placements
     * the metadata named and no advert image was supplied for, which is what a mismatch between
     * the creative names and the producer's placement names looks like.
     *
     * augmented_placements counts PLACEMENTS, not frames: a frame carrying three adverts adds
     * three, so divided by frames_augmented it is the average number of adverts on a frame.
     *
     * frames_fully_occluded is the other way a drawn advert fails to appear - it was drawn and
     * then covered entirely by the mask, so it counts towards augmented_placements rather than
     * towards any of the skips.
     */
    uint64_t augmented_placements;
    uint64_t skipped_no_creative;
    uint64_t skipped_no_mask;
    uint64_t not_live;
    uint64_t frames_fully_occluded;

    /**
     * Time spent inside sfa_augment, in microseconds, over the frames that were drawn on.
     * Divide the total by frames_augmented for the mean; the maximum is the one that decides
     * whether a frame was ever late.
     */
    uint64_t augment_time_us_total;
    uint64_t augment_time_us_max;
} sfa_stats;

/**
 * The version of the library this build is compiled against, at runtime.
 *
 * Compare it with SFA_ABI_VERSION, which comes from this header, before sfa_init 
 * and treat a difference as fatal.
 */
SFA_API uint32_t sfa_abi_version(void);

/**
 * What this build is, in words, for a log line or a bug report.
 * Never NULL, owned by the library, and valid for the life of the process. It answers "which
 * build is this" and is not a compatibility check - use sfa_abi_version() for that.
 */
SFA_API const char* sfa_build_info(void);

/**
 * Creates a node.
 *
 * Allocates device memory, uploads the advert images and initialises the renderer, so this
 * call is substantially more expensive than sfa_augment. On failure *out is set to NULL.
 */
SFA_API sfa_status sfa_init(const sfa_init_params* params, sfa_node** out);

/**
 * Augments one frame, in place.
 *
 * The frame's pixels are rewritten where an advert is drawn and left alone everywhere else.
 * SFA_OK means the frame changed; SFA_UNMODIFIED means it is exactly as it was passed in.
 * SFA_FRAME_DAMAGED means it is neither and must be discarded. Every other result leaves the
 * frame untouched and usable.
 *
 * Runs entirely on the calling thread and creates no threads. A node must not be used
 * concurrently from more than one thread; separate nodes are independent.
 *
 * frame and metadata, including the pixels and mask they point to, must remain valid for the
 * duration of the call. The metadata is not modified.
 */
SFA_API sfa_status sfa_augment(sfa_node* node, sfa_image* frame, const sfa_frame_metadata* metadata);

/**
 * Fills `out` with what this node has done.
 *
 * A host is expected to log these on its own cadence rather than have the library pick one. The
 * library writes a single summary line through log_cb when the node is destroyed.
 */
SFA_API sfa_status sfa_get_stats(sfa_node* node, sfa_stats* out);

/** Destroys a node. Passing NULL is permitted. */
SFA_API void sfa_destroy(sfa_node* node);

/**
 * Returns a description of the most recent failure on this node, or an empty string. Never
 * NULL. The pointer remains valid until the next call on the same node.
 */
SFA_API const char* sfa_last_error(sfa_node* node);

#ifdef __cplusplus
}
#endif

#endif
