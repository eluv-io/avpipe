/*
 * mvhevc_apple.mm - Mac-only MV-HEVC encoder using AVFoundation/VideoToolbox.
 *
 * This tool accepts the same command-line parameters as mvhevc_encoder, but
 * writes a directly muxed .mov/.mp4 instead of a raw Annex B .hevc stream.
 */

#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <Foundation/Foundation.h>
#import <VideoToolbox/VideoToolbox.h>

#include <ctype.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_VIDEO_BITRATE_KBPS 400000

typedef struct apple_params {
    const char *left_file;
    const char *right_file;
    const char *out_file;
    const char *crf;
    const char *preset;
    const char *tune;
    const char *profile;
    const char *max_cll;
    const char *master_display;
    int bitrate_kbps;
    int maxrate_kbps;
    int bufsize_kbits;
    int width;
    int height;
    int keyint;
    int bframes;
    int level;
    int high_tier;
    int bitdepth;
    int hdr;
    int scenecut;
    int fps_num;
    int fps_den;
    int saw_x265_only;
    double duration_seconds;
} apple_params;

static void usage(const char *prog)
{
    fprintf(stderr,
        "Usage: %s [options] <left_eye> <right_eye> <output.mov|mp4>\n"
        "       %s [options] -i <mvhevc_input> <output.mov|mp4>\n"
        "\n"
        "Encodes stereo MV-HEVC using Apple AVFoundation/VideoToolbox.\n"
        "\n"
        "Input modes:\n"
        "  Two files:   <left_eye> <right_eye>  (separate eye inputs)\n"
        "  Single file: -i <input.mp4>          (not implemented in Apple path)\n"
        "\n"
        "Options:\n"
        "  -crf <val>          Accepted for CLI compatibility; ignored\n"
        "  -bitrate <kbps>     Target average bitrate, max 400000 kbps\n"
        "  -maxrate <kbps>     Hard data-rate limit over 1 second\n"
        "  -bufsize <kbits>    Accepted for CLI compatibility; ignored\n"
        "  -w <pixels>         Output width\n"
        "  -h <pixels>         Output height (width auto-computed if -w omitted)\n"
        "  -keyint <frames>    Max keyframe interval\n"
        "  -bframes <n>        0 disables frame reordering; >0 enables it\n"
        "  -preset <name>      Accepted for CLI compatibility; ignored\n"
        "  -tune <name>        Accepted for CLI compatibility; ignored\n"
        "  -profile <name>     HEVC profile hint: main or main10\n"
        "  -level <val>        Accepted for CLI compatibility; ignored\n"
        "  -hightier           Accepted for CLI compatibility; ignored\n"
        "  -bitdepth <n>       Bit depth: 8 or 10 (default 8)\n"
        "  -hdr                Enable HDR10 BT.2020/PQ color and metadata\n"
        "  -scenecut <n>       Accepted for CLI compatibility; ignored\n"
        "  -fps <num/den>      Override output framerate timestamps\n"
        "  -duration <sec>     Encode at most this many seconds\n"
        "  -max-cll <val>      HDR MaxCLL/MaxFALL, e.g. \"1000,200\"\n"
        "  -master-display <v> HDR master display string\n"
        "\n",
        prog, prog);
}

static void set_defaults(apple_params *p)
{
    memset(p, 0, sizeof(*p));
    p->crf = "23";
    p->preset = "medium";
    p->bitdepth = 8;
    p->bframes = -1;
    p->scenecut = 40;
}

static int parse_positive_double(const char *s, double *out)
{
    char *end = NULL;
    double v = strtod(s, &end);
    while (end && *end && isspace((unsigned char)*end))
        end++;
    if (end == s || !end || *end != '\0' || !isfinite(v) || v <= 0.0)
        return -1;
    *out = v;
    return 0;
}

static int parse_args(int argc, char **argv, apple_params *p)
{
    set_defaults(p);

    const char *single_input = NULL;
    int argi = 1;
    while (argi < argc && argv[argi][0] == '-') {
        const char *opt = argv[argi++];
        if (!strcmp(opt, "-i") && argi < argc) {
            single_input = argv[argi++];
        } else if (!strcmp(opt, "-crf") && argi < argc) {
            p->crf = argv[argi++];
            p->saw_x265_only = 1;
        } else if (!strcmp(opt, "-bitrate") && argi < argc) {
            p->bitrate_kbps = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-maxrate") && argi < argc) {
            p->maxrate_kbps = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-bufsize") && argi < argc) {
            p->bufsize_kbits = atoi(argv[argi++]);
            p->saw_x265_only = 1;
        } else if (!strcmp(opt, "-w") && argi < argc) {
            p->width = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-h") && argi < argc) {
            p->height = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-keyint") && argi < argc) {
            p->keyint = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-bframes") && argi < argc) {
            p->bframes = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-preset") && argi < argc) {
            p->preset = argv[argi++];
            p->saw_x265_only = 1;
        } else if (!strcmp(opt, "-tune") && argi < argc) {
            p->tune = argv[argi++];
            p->saw_x265_only = 1;
        } else if (!strcmp(opt, "-profile") && argi < argc) {
            p->profile = argv[argi++];
        } else if (!strcmp(opt, "-level") && argi < argc) {
            p->level = atoi(argv[argi++]);
            p->saw_x265_only = 1;
        } else if (!strcmp(opt, "-hightier")) {
            p->high_tier = 1;
            p->saw_x265_only = 1;
        } else if (!strcmp(opt, "-fps") && argi < argc) {
            if (sscanf(argv[argi], "%d/%d", &p->fps_num, &p->fps_den) != 2 ||
                p->fps_num <= 0 || p->fps_den <= 0) {
                fprintf(stderr, "Invalid fps '%s', expected num/den\n", argv[argi]);
                return -1;
            }
            argi++;
        } else if (!strcmp(opt, "-duration") && argi < argc) {
            if (parse_positive_double(argv[argi], &p->duration_seconds) < 0) {
                fprintf(stderr, "Invalid duration '%s', expected positive seconds\n", argv[argi]);
                return -1;
            }
            argi++;
        } else if (!strcmp(opt, "-max-cll") && argi < argc) {
            p->max_cll = argv[argi++];
        } else if (!strcmp(opt, "-master-display") && argi < argc) {
            p->master_display = argv[argi++];
        } else if (!strcmp(opt, "-bitdepth") && argi < argc) {
            p->bitdepth = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-hdr")) {
            p->hdr = 1;
        } else if (!strcmp(opt, "-scenecut") && argi < argc) {
            p->scenecut = atoi(argv[argi++]);
            p->saw_x265_only = 1;
        } else {
            fprintf(stderr, "Unknown option: %s\n", opt);
            return -1;
        }
    }

    if (single_input) {
        fprintf(stderr, "Apple encoder does not implement -i single-input re-encode mode yet\n");
        return -1;
    }
    if (argc - argi != 3) {
        fprintf(stderr, "Expected 3 positional args: <left_eye> <right_eye> <output.mov|mp4>\n");
        return -1;
    }

    p->left_file = argv[argi];
    p->right_file = argv[argi + 1];
    p->out_file = argv[argi + 2];
    if (p->bitdepth != 8 && p->bitdepth != 10) {
        fprintf(stderr, "Invalid bitdepth %d, expected 8 or 10\n", p->bitdepth);
        return -1;
    }
    if (p->bitrate_kbps > MAX_VIDEO_BITRATE_KBPS) {
        fprintf(stderr, "Invalid bitrate %d kbps: maximum is %d kbps (400 Mbps)\n",
                p->bitrate_kbps, MAX_VIDEO_BITRATE_KBPS);
        return -1;
    }
    if (p->profile && !strcmp(p->profile, "main10") && p->bitdepth != 10) {
        fprintf(stderr, "Profile main10 requires -bitdepth 10 for the Apple encoder\n");
        return -1;
    }
    if (p->hdr && p->bitdepth != 10) {
        fprintf(stderr, "HDR output requires -bitdepth 10 for the Apple encoder\n");
        return -1;
    }
    return 0;
}

static void put_be16(uint8_t *dst, uint16_t v)
{
    dst[0] = (uint8_t)(v >> 8);
    dst[1] = (uint8_t)(v & 0xff);
}

static void put_be32(uint8_t *dst, uint32_t v)
{
    dst[0] = (uint8_t)(v >> 24);
    dst[1] = (uint8_t)((v >> 16) & 0xff);
    dst[2] = (uint8_t)((v >> 8) & 0xff);
    dst[3] = (uint8_t)(v & 0xff);
}

static NSData *master_display_data(const char *s)
{
    unsigned gx, gy, bx, by, rx, ry, wx, wy, max_lum, min_lum;
    if (!s)
        return nil;
    if (sscanf(s, "G(%u,%u)B(%u,%u)R(%u,%u)WP(%u,%u)L(%u,%u)",
               &gx, &gy, &bx, &by, &rx, &ry, &wx, &wy, &max_lum, &min_lum) != 10) {
        fprintf(stderr, "Warning: could not parse -master-display, skipping MDCV metadata\n");
        return nil;
    }

    uint8_t bytes[24];
    put_be16(bytes + 0, (uint16_t)gx);
    put_be16(bytes + 2, (uint16_t)gy);
    put_be16(bytes + 4, (uint16_t)bx);
    put_be16(bytes + 6, (uint16_t)by);
    put_be16(bytes + 8, (uint16_t)rx);
    put_be16(bytes + 10, (uint16_t)ry);
    put_be16(bytes + 12, (uint16_t)wx);
    put_be16(bytes + 14, (uint16_t)wy);
    put_be32(bytes + 16, max_lum);
    put_be32(bytes + 20, min_lum);
    return [NSData dataWithBytes:bytes length:sizeof(bytes)];
}

static NSData *content_light_data(const char *s)
{
    unsigned max_cll, max_fall;
    if (!s)
        return nil;
    if (sscanf(s, "%u,%u", &max_cll, &max_fall) != 2) {
        fprintf(stderr, "Warning: could not parse -max-cll, skipping CLLI metadata\n");
        return nil;
    }

    uint8_t bytes[4];
    put_be16(bytes + 0, (uint16_t)max_cll);
    put_be16(bytes + 2, (uint16_t)max_fall);
    return [NSData dataWithBytes:bytes length:sizeof(bytes)];
}

static NSString *file_type_for_output(NSString *path)
{
    NSString *ext = [[path pathExtension] lowercaseString];
    if ([ext isEqualToString:@"mp4"] || [ext isEqualToString:@"m4v"])
        return AVFileTypeMPEG4;
    return AVFileTypeQuickTimeMovie;
}

static BOOL wait_until_ready(AVAssetWriterInput *input, AVAssetWriter *writer)
{
    while (!input.readyForMoreMediaData) {
        if (writer.status == AVAssetWriterStatusFailed ||
            writer.status == AVAssetWriterStatusCancelled)
            return NO;
        [NSThread sleepForTimeInterval:0.001];
    }
    return YES;
}

static BOOL copy_to_pool(CVPixelBufferPoolRef pool,
                         VTPixelTransferSessionRef transfer,
                         CVImageBufferRef src,
                         BOOL hdr,
                         CVPixelBufferRef *dst_out)
{
    CVPixelBufferRef dst = NULL;
    CVReturn cv = CVPixelBufferPoolCreatePixelBuffer(kCFAllocatorDefault, pool, &dst);
    if (cv != kCVReturnSuccess) {
        fprintf(stderr, "CVPixelBufferPoolCreatePixelBuffer failed: %d\n", cv);
        return NO;
    }

    OSStatus st = VTPixelTransferSessionTransferImage(transfer, src, dst);
    if (st != noErr) {
        fprintf(stderr, "VTPixelTransferSessionTransferImage failed: %d\n", (int)st);
        CVPixelBufferRelease(dst);
        return NO;
    }

    if (hdr) {
        CVBufferSetAttachment(dst, kCVImageBufferColorPrimariesKey,
                              kCVImageBufferColorPrimaries_ITU_R_2020,
                              kCVAttachmentMode_ShouldPropagate);
        CVBufferSetAttachment(dst, kCVImageBufferTransferFunctionKey,
                              kCVImageBufferTransferFunction_SMPTE_ST_2084_PQ,
                              kCVAttachmentMode_ShouldPropagate);
        CVBufferSetAttachment(dst, kCVImageBufferYCbCrMatrixKey,
                              kCVImageBufferYCbCrMatrix_ITU_R_2020,
                              kCVAttachmentMode_ShouldPropagate);
    }

    *dst_out = dst;
    return YES;
}

static CMTaggedBufferGroupRef create_tagged_group(CVPixelBufferRef left, CVPixelBufferRef right)
{
    CMTag left_tags[2] = {
        CMTagMakeWithSInt64Value(kCMTagCategory_VideoLayerID, 0),
        kCMTagStereoLeftEye,
    };
    CMTag right_tags[2] = {
        CMTagMakeWithSInt64Value(kCMTagCategory_VideoLayerID, 1),
        kCMTagStereoRightEye,
    };

    CMTagCollectionRef left_collection = NULL;
    CMTagCollectionRef right_collection = NULL;
    OSStatus st = CMTagCollectionCreate(kCFAllocatorDefault, left_tags, 2, &left_collection);
    if (st != noErr) {
        fprintf(stderr, "CMTagCollectionCreate(left) failed: %d\n", (int)st);
        return NULL;
    }
    st = CMTagCollectionCreate(kCFAllocatorDefault, right_tags, 2, &right_collection);
    if (st != noErr) {
        fprintf(stderr, "CMTagCollectionCreate(right) failed: %d\n", (int)st);
        CFRelease(left_collection);
        return NULL;
    }

    NSArray *tag_collections = @[(__bridge id)left_collection, (__bridge id)right_collection];
    NSArray *buffers = @[(__bridge id)left, (__bridge id)right];
    CMTaggedBufferGroupRef group = NULL;
    st = CMTaggedBufferGroupCreate(kCFAllocatorDefault,
                                   (__bridge CFArrayRef)tag_collections,
                                   (__bridge CFArrayRef)buffers,
                                   &group);

    CFRelease(left_collection);
    CFRelease(right_collection);
    if (st != noErr) {
        fprintf(stderr, "CMTaggedBufferGroupCreate failed: %d\n", (int)st);
        return NULL;
    }
    return group;
}

static int encode_apple(const apple_params *p)
{
    if (@available(macOS 14.0, *)) {
        if (!VTIsStereoMVHEVCEncodeSupported()) {
            fprintf(stderr, "Stereo MV-HEVC encode is not supported on this Mac\n");
            return 1;
        }
    } else {
        fprintf(stderr, "Apple MV-HEVC encode requires macOS 14.0 or newer\n");
        return 1;
    }

    NSString *left_path = [NSString stringWithUTF8String:p->left_file];
    NSString *right_path = [NSString stringWithUTF8String:p->right_file];
    NSString *out_path = [NSString stringWithUTF8String:p->out_file];
    NSURL *left_url = [NSURL fileURLWithPath:left_path];
    NSURL *right_url = [NSURL fileURLWithPath:right_path];
    NSURL *out_url = [NSURL fileURLWithPath:out_path];

    AVURLAsset *left_asset = [AVURLAsset URLAssetWithURL:left_url options:nil];
    AVURLAsset *right_asset = [AVURLAsset URLAssetWithURL:right_url options:nil];
    AVAssetTrack *left_track = [[left_asset tracksWithMediaType:AVMediaTypeVideo] firstObject];
    AVAssetTrack *right_track = [[right_asset tracksWithMediaType:AVMediaTypeVideo] firstObject];
    if (!left_track || !right_track) {
        fprintf(stderr, "Could not find video tracks in both inputs\n");
        return 1;
    }

    CGSize left_size = left_track.naturalSize;
    CGSize right_size = right_track.naturalSize;
    int src_w = (int)llround(fabs(left_size.width));
    int src_h = (int)llround(fabs(left_size.height));
    int right_w = (int)llround(fabs(right_size.width));
    int right_h = (int)llround(fabs(right_size.height));
    if (src_w <= 0 || src_h <= 0 || right_w <= 0 || right_h <= 0) {
        fprintf(stderr, "Invalid source dimensions\n");
        return 1;
    }
    if (src_w != right_w || src_h != right_h) {
        fprintf(stderr, "Left/right dimensions differ: %dx%d vs %dx%d\n",
                src_w, src_h, right_w, right_h);
        return 1;
    }

    int out_w = p->width;
    int out_h = p->height;
    if (out_w <= 0 && out_h <= 0) {
        out_w = src_w;
        out_h = src_h;
    } else if (out_w > 0 && out_h <= 0) {
        out_h = (int)llround((double)out_w * (double)src_h / (double)src_w);
    } else if (out_h > 0 && out_w <= 0) {
        out_w = (int)llround((double)out_h * (double)src_w / (double)src_h);
    }
    if ((out_w & 1) || (out_h & 1)) {
        fprintf(stderr, "Output dimensions must be even for 4:2:0 HEVC (got %dx%d)\n", out_w, out_h);
        return 1;
    }

    OSType writer_pixel_format = p->bitdepth == 10 ?
        kCVPixelFormatType_420YpCbCr10BiPlanarVideoRange :
        kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;
    OSType reader_pixel_format = p->bitdepth == 10 ?
        kCVPixelFormatType_422YpCbCr10 :
        kCVPixelFormatType_32BGRA;

    NSDictionary *reader_settings = @{
        (__bridge NSString *)kCVPixelBufferPixelFormatTypeKey: @(reader_pixel_format),
        (__bridge NSString *)kCVPixelBufferIOSurfacePropertiesKey: @{},
    };

    NSError *error = nil;
    AVAssetReader *left_reader = [[AVAssetReader alloc] initWithAsset:left_asset error:&error];
    if (!left_reader) {
        fprintf(stderr, "Could not create left reader: %s\n", error.localizedDescription.UTF8String);
        return 1;
    }
    AVAssetReader *right_reader = [[AVAssetReader alloc] initWithAsset:right_asset error:&error];
    if (!right_reader) {
        fprintf(stderr, "Could not create right reader: %s\n", error.localizedDescription.UTF8String);
        return 1;
    }

    AVAssetReaderTrackOutput *left_output =
        [[AVAssetReaderTrackOutput alloc] initWithTrack:left_track outputSettings:reader_settings];
    AVAssetReaderTrackOutput *right_output =
        [[AVAssetReaderTrackOutput alloc] initWithTrack:right_track outputSettings:reader_settings];
    left_output.alwaysCopiesSampleData = NO;
    right_output.alwaysCopiesSampleData = NO;
    if (![left_reader canAddOutput:left_output] || ![right_reader canAddOutput:right_output]) {
        fprintf(stderr, "Could not add reader outputs\n");
        return 1;
    }
    [left_reader addOutput:left_output];
    [right_reader addOutput:right_output];

    [[NSFileManager defaultManager] removeItemAtURL:out_url error:nil];
    NSString *file_type = file_type_for_output(out_path);
    AVAssetWriter *writer = [[AVAssetWriter alloc] initWithURL:out_url fileType:file_type error:&error];
    if (!writer) {
        fprintf(stderr, "Could not create writer: %s\n", error.localizedDescription.UTF8String);
        return 1;
    }

    NSMutableDictionary *compression = [NSMutableDictionary dictionary];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_MVHEVCVideoLayerIDs] = @[@0, @1];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_MVHEVCViewIDs] = @[@0, @1];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_MVHEVCLeftAndRightViewIDs] = @[@0, @1];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_HasLeftStereoEyeView] = @YES;
    compression[(__bridge NSString *)kVTCompressionPropertyKey_HasRightStereoEyeView] = @YES;
    compression[AVVideoProfileLevelKey] =
        p->bitdepth == 10 || (p->profile && !strcmp(p->profile, "main10")) ?
        (__bridge NSString *)kVTProfileLevel_HEVC_Main10_AutoLevel :
        (__bridge NSString *)kVTProfileLevel_HEVC_Main_AutoLevel;

    if (p->bitrate_kbps > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_AverageBitRate] = @(p->bitrate_kbps * 1000);
    if (p->maxrate_kbps > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_DataRateLimits] = @[@((p->maxrate_kbps * 1000) / 8), @1];
    if (p->keyint > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_MaxKeyFrameInterval] = @(p->keyint);
    if (p->bframes >= 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_AllowFrameReordering] = p->bframes == 0 ? @NO : @YES;
    if (p->fps_num > 0 && p->fps_den > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_ExpectedFrameRate] = @((double)p->fps_num / (double)p->fps_den);

    NSMutableDictionary *output_settings = [@{
        AVVideoCodecKey: AVVideoCodecTypeHEVC,
        AVVideoWidthKey: @(out_w),
        AVVideoHeightKey: @(out_h),
        AVVideoCompressionPropertiesKey: compression,
    } mutableCopy];

    if (p->hdr) {
        NSDictionary *color_props = @{
            AVVideoColorPrimariesKey: AVVideoColorPrimaries_ITU_R_2020,
            AVVideoTransferFunctionKey: AVVideoTransferFunction_SMPTE_ST_2084_PQ,
            AVVideoYCbCrMatrixKey: AVVideoYCbCrMatrix_ITU_R_2020,
        };
        output_settings[AVVideoColorPropertiesKey] = color_props;
        compression[(__bridge NSString *)kVTCompressionPropertyKey_ColorPrimaries] =
            (__bridge NSString *)kCMFormatDescriptionColorPrimaries_ITU_R_2020;
        compression[(__bridge NSString *)kVTCompressionPropertyKey_TransferFunction] =
            (__bridge NSString *)kCMFormatDescriptionTransferFunction_SMPTE_ST_2084_PQ;
        compression[(__bridge NSString *)kVTCompressionPropertyKey_YCbCrMatrix] =
            (__bridge NSString *)kCMFormatDescriptionYCbCrMatrix_ITU_R_2020;
        compression[(__bridge NSString *)kVTCompressionPropertyKey_HDRMetadataInsertionMode] =
            (__bridge NSString *)kVTHDRMetadataInsertionMode_Auto;

        NSData *mdcv = master_display_data(p->master_display);
        NSData *clli = content_light_data(p->max_cll);
        if (mdcv)
            compression[(__bridge NSString *)kVTCompressionPropertyKey_MasteringDisplayColorVolume] = mdcv;
        if (clli)
            compression[(__bridge NSString *)kVTCompressionPropertyKey_ContentLightLevelInfo] = clli;
    }

    if (![writer canApplyOutputSettings:output_settings forMediaType:AVMediaTypeVideo]) {
        fprintf(stderr, "Writer cannot apply requested MV-HEVC output settings\n");
        return 1;
    }

    AVAssetWriterInput *writer_input =
        [[AVAssetWriterInput alloc] initWithMediaType:AVMediaTypeVideo outputSettings:output_settings];
    writer_input.expectsMediaDataInRealTime = NO;

    NSDictionary *source_attrs = @{
        (__bridge NSString *)kCVPixelBufferPixelFormatTypeKey: @(writer_pixel_format),
        (__bridge NSString *)kCVPixelBufferWidthKey: @(out_w),
        (__bridge NSString *)kCVPixelBufferHeightKey: @(out_h),
        (__bridge NSString *)kCVPixelBufferIOSurfacePropertiesKey: @{},
    };
    AVAssetWriterInputTaggedPixelBufferGroupAdaptor *adaptor =
        [[AVAssetWriterInputTaggedPixelBufferGroupAdaptor alloc] initWithAssetWriterInput:writer_input
                                                             sourcePixelBufferAttributes:source_attrs];

    if (![writer canAddInput:writer_input]) {
        fprintf(stderr, "Could not add writer input\n");
        return 1;
    }
    [writer addInput:writer_input];

    if (![left_reader startReading] || ![right_reader startReading]) {
        fprintf(stderr, "Could not start readers: left=%s right=%s\n",
                left_reader.error.localizedDescription.UTF8String ?: "ok",
                right_reader.error.localizedDescription.UTF8String ?: "ok");
        return 1;
    }
    if (![writer startWriting]) {
        fprintf(stderr, "Could not start writer: %s\n", writer.error.localizedDescription.UTF8String);
        return 1;
    }
    [writer startSessionAtSourceTime:kCMTimeZero];

    CVPixelBufferPoolRef pool = adaptor.pixelBufferPool;
    if (!pool) {
        fprintf(stderr, "Writer adaptor did not provide a pixel buffer pool\n");
        return 1;
    }

    VTPixelTransferSessionRef transfer = NULL;
    OSStatus st = VTPixelTransferSessionCreate(kCFAllocatorDefault, &transfer);
    if (st != noErr) {
        fprintf(stderr, "VTPixelTransferSessionCreate failed: %d\n", (int)st);
        return 1;
    }
    VTSessionSetProperty(transfer, kVTPixelTransferPropertyKey_ScalingMode, kVTScalingMode_Normal);
    if (p->hdr) {
        VTSessionSetProperty(transfer, kVTPixelTransferPropertyKey_DestinationColorPrimaries,
                             kCMFormatDescriptionColorPrimaries_ITU_R_2020);
        VTSessionSetProperty(transfer, kVTPixelTransferPropertyKey_DestinationTransferFunction,
                             kCMFormatDescriptionTransferFunction_SMPTE_ST_2084_PQ);
        VTSessionSetProperty(transfer, kVTPixelTransferPropertyKey_DestinationYCbCrMatrix,
                             kCMFormatDescriptionYCbCrMatrix_ITU_R_2020);
    }

    fprintf(stderr, "Apple MV-HEVC encoder configuration:\n");
    fprintf(stderr, "  Input:       %dx%d\n", src_w, src_h);
    fprintf(stderr, "  Output:      %dx%d %s\n", out_w, out_h, file_type.UTF8String);
    fprintf(stderr, "  Bit depth:   %d\n", p->bitdepth);
    if (p->fps_num > 0)
        fprintf(stderr, "  FPS:         %d/%d\n", p->fps_num, p->fps_den);
    if (p->bitrate_kbps > 0)
        fprintf(stderr, "  Bitrate:     %d kbps\n", p->bitrate_kbps);
    if (p->duration_seconds > 0.0)
        fprintf(stderr, "  Duration:    %.3f seconds\n", p->duration_seconds);
    if (p->hdr)
        fprintf(stderr, "  HDR10:       BT.2020/PQ + optional MDCV/CLLI\n");
    if (p->saw_x265_only)
        fprintf(stderr, "  Note:        x265-only options were accepted but ignored by the Apple encoder\n");

    int64_t frame_count = 0;
    CMTime first_pts = kCMTimeInvalid;
    CMTime duration_limit = p->duration_seconds > 0.0 ?
        CMTimeMakeWithSeconds(p->duration_seconds, 1000000) :
        kCMTimeInvalid;
    BOOL ok = YES;

    for (;;) {
        if (!wait_until_ready(writer_input, writer)) {
            fprintf(stderr, "Writer failed while waiting: %s\n", writer.error.localizedDescription.UTF8String);
            ok = NO;
            break;
        }

        CMSampleBufferRef left_sample = [left_output copyNextSampleBuffer];
        CMSampleBufferRef right_sample = [right_output copyNextSampleBuffer];
        if (!left_sample && !right_sample)
            break;
        if (!left_sample || !right_sample) {
            fprintf(stderr, "Left/right input sample counts differ\n");
            if (left_sample)
                CFRelease(left_sample);
            if (right_sample)
                CFRelease(right_sample);
            ok = NO;
            break;
        }

        CVImageBufferRef left_src = CMSampleBufferGetImageBuffer(left_sample);
        CVImageBufferRef right_src = CMSampleBufferGetImageBuffer(right_sample);
        if (!left_src || !right_src) {
            fprintf(stderr, "Could not get decoded image buffers\n");
            CFRelease(left_sample);
            CFRelease(right_sample);
            ok = NO;
            break;
        }

        CMTime pts;
        if (p->fps_num > 0 && p->fps_den > 0) {
            pts = CMTimeMake(frame_count * (int64_t)p->fps_den, p->fps_num);
        } else {
            pts = CMSampleBufferGetPresentationTimeStamp(left_sample);
            if (!CMTIME_IS_VALID(pts))
                pts = CMTimeMake(frame_count, left_track.nominalFrameRate > 0 ? (int32_t)llround(left_track.nominalFrameRate) : 30);
            if (!CMTIME_IS_VALID(first_pts))
                first_pts = pts;
            pts = CMTimeSubtract(pts, first_pts);
        }

        if (CMTIME_IS_VALID(duration_limit) && CMTimeCompare(pts, duration_limit) >= 0) {
            CFRelease(left_sample);
            CFRelease(right_sample);
            break;
        }

        CVPixelBufferRef left_dst = NULL;
        CVPixelBufferRef right_dst = NULL;
        if (!copy_to_pool(pool, transfer, left_src, p->hdr, &left_dst) ||
            !copy_to_pool(pool, transfer, right_src, p->hdr, &right_dst)) {
            if (left_dst)
                CVPixelBufferRelease(left_dst);
            if (right_dst)
                CVPixelBufferRelease(right_dst);
            CFRelease(left_sample);
            CFRelease(right_sample);
            ok = NO;
            break;
        }

        CMTaggedBufferGroupRef group = create_tagged_group(left_dst, right_dst);
        if (!group) {
            CVPixelBufferRelease(left_dst);
            CVPixelBufferRelease(right_dst);
            CFRelease(left_sample);
            CFRelease(right_sample);
            ok = NO;
            break;
        }

        BOOL appended = [adaptor appendTaggedPixelBufferGroup:group withPresentationTime:pts];
        CFRelease(group);
        CVPixelBufferRelease(left_dst);
        CVPixelBufferRelease(right_dst);
        CFRelease(left_sample);
        CFRelease(right_sample);

        if (!appended) {
            fprintf(stderr, "appendTaggedPixelBufferGroup failed: %s\n",
                    writer.error.localizedDescription.UTF8String ?: "unknown writer error");
            ok = NO;
            break;
        }

        frame_count++;
        if (frame_count % 100 == 0)
            fprintf(stderr, "Encoded %lld frames\n", (long long)frame_count);
    }

    if (transfer)
        CFRelease(transfer);

    [writer_input markAsFinished];
    if (!ok) {
        [writer cancelWriting];
        return 1;
    }

    __block BOOL finished = NO;
    __block BOOL finish_ok = NO;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [writer finishWritingWithCompletionHandler:^{
        finish_ok = writer.status == AVAssetWriterStatusCompleted;
        finished = YES;
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

    if (!finished || !finish_ok) {
        fprintf(stderr, "finishWriting failed: %s\n",
                writer.error.localizedDescription.UTF8String ?: "unknown writer error");
        return 1;
    }

    fprintf(stderr, "Encoded %lld frames\n", (long long)frame_count);
    fprintf(stderr, "Output: %s\n", p->out_file);
    return 0;
}

int main(int argc, char **argv)
{
    @autoreleasepool {
        if (argc < 3) {
            usage(argv[0]);
            return 1;
        }

        apple_params params;
        if (parse_args(argc, argv, &params) < 0) {
            usage(argv[0]);
            return 1;
        }

        @try {
            return encode_apple(&params);
        } @catch (NSException *exception) {
            fprintf(stderr, "Objective-C exception: %s: %s\n",
                    exception.name.UTF8String,
                    exception.reason.UTF8String ?: "");
            return 1;
        }
    }
}
