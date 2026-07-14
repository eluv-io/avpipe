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

typedef struct apple_output_config {
    char *out_file;
    char *profile;
    int bitrate_kbps;
    int maxrate_kbps;
    int width;
    int height;
    int level;
    int bitdepth;
} apple_output_config;

typedef struct apple_params {
    const char *left_file;
    const char *right_file;
    const char *out_file;
    const char *abr_profile_file;
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
    int no_upscale;
    double duration_seconds;
    double quality;
    apple_output_config *outputs;
    int output_count;
} apple_params;

@interface AppleOutputContext : NSObject
@property(nonatomic, assign) apple_output_config *config;
@property(nonatomic, strong) NSString *outPath;
@property(nonatomic, strong) NSString *fileType;
@property(nonatomic, strong) AVAssetWriter *writer;
@property(nonatomic, strong) AVAssetWriterInput *writerInput;
@property(nonatomic, strong) AVAssetWriterInputTaggedPixelBufferGroupAdaptor *adaptor;
@property(nonatomic, strong) NSDictionary<NSString *, NSString *> *colorProperties;
@property(nonatomic, assign) CVPixelBufferPoolRef pool;
@property(nonatomic, assign) VTPixelTransferSessionRef transfer;
@property(nonatomic, assign) int outWidth;
@property(nonatomic, assign) int outHeight;
@property(nonatomic, assign) int64_t frameCount;
@end

@implementation AppleOutputContext
- (void)dealloc
{
    if (_transfer)
        CFRelease(_transfer);
}
@end

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
        "  -quality <0.0-1.0>  VideoToolbox compression quality hint\n"
        "  -bufsize <kbits>    Accepted for CLI compatibility; ignored\n"
        "  -w <pixels>         Output width\n"
        "  -h <pixels>         Output height (width auto-computed if -w omitted)\n"
        "  -keyint <frames>    Keyframe interval\n"
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
        "  -abr-profile <json> Encode all video rungs from an ABR profile\n"
        "\n"
        "With -abr-profile, <output.mov|mp4> is used as a base/template. If it\n"
        "contains %%w, %%h, %%b, %%m, or %%n, those placeholders are expanded to\n"
        "width, height, kbps, Mbps, and 1-based rung index. Otherwise the tool\n"
        "adds _<width>x<height>@<Mbps> before the extension.\n"
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
    p->quality = -1.0;
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

static int parse_quality_double(const char *s, double *out)
{
    char *end = NULL;
    double v = strtod(s, &end);
    while (end && *end && isspace((unsigned char)*end))
        end++;
    if (end == s || !end || *end != '\0' || !isfinite(v) || v < 0.0 || v > 1.0)
        return -1;
    *out = v;
    return 0;
}

static int parse_level_value(const char *s)
{
    if (!s || !*s)
        return 0;
    int major = 0;
    int minor = 0;
    if (sscanf(s, "%d.%d", &major, &minor) == 2)
        return major * 10 + minor;
    return atoi(s);
}

static int level_from_json(id value)
{
    if (!value || value == (id)kCFNull)
        return 0;
    if ([value isKindOfClass:[NSNumber class]])
        return [(NSNumber *)value intValue];
    if ([value isKindOfClass:[NSString class]])
        return parse_level_value([(NSString *)value UTF8String]);
    return 0;
}

static char *strdup_nsstring(NSString *s)
{
    if (!s)
        return NULL;
    return strdup(s.UTF8String);
}

static NSString *abr_output_path(NSString *base, const apple_output_config *cfg, int index)
{
    NSString *mbps = [NSString stringWithFormat:@"%.2f", (double)cfg->bitrate_kbps / 1000.0];
    if ([base containsString:@"%w"] || [base containsString:@"%h"] ||
        [base containsString:@"%b"] || [base containsString:@"%m"] ||
        [base containsString:@"%n"]) {
        NSString *out = [base copy];
        out = [out stringByReplacingOccurrencesOfString:@"%w" withString:[NSString stringWithFormat:@"%d", cfg->width]];
        out = [out stringByReplacingOccurrencesOfString:@"%h" withString:[NSString stringWithFormat:@"%d", cfg->height]];
        out = [out stringByReplacingOccurrencesOfString:@"%b" withString:[NSString stringWithFormat:@"%d", cfg->bitrate_kbps]];
        out = [out stringByReplacingOccurrencesOfString:@"%m" withString:mbps];
        out = [out stringByReplacingOccurrencesOfString:@"%n" withString:[NSString stringWithFormat:@"%d", index + 1]];
        return out;
    }

    NSString *ext = [base pathExtension];
    NSString *stem = [base stringByDeletingPathExtension];
    NSString *with_suffix = [stem stringByAppendingFormat:@"_%dx%d@%@", cfg->width, cfg->height, mbps];
    if (ext.length > 0)
        return [with_suffix stringByAppendingPathExtension:ext];
    return with_suffix;
}

static int validate_output_config(const apple_params *p, const apple_output_config *cfg)
{
    if (cfg->bitdepth != 8 && cfg->bitdepth != 10) {
        fprintf(stderr, "Invalid bitdepth %d, expected 8 or 10\n", cfg->bitdepth);
        return -1;
    }
    if (cfg->bitrate_kbps > MAX_VIDEO_BITRATE_KBPS) {
        fprintf(stderr, "Invalid bitrate %d kbps: maximum is %d kbps (400 Mbps)\n",
                cfg->bitrate_kbps, MAX_VIDEO_BITRATE_KBPS);
        return -1;
    }
    if (cfg->profile && !strcmp(cfg->profile, "main10") && cfg->bitdepth != 10) {
        fprintf(stderr, "Profile main10 requires bitdepth 10 for the Apple encoder\n");
        return -1;
    }
    if (p->hdr && cfg->bitdepth != 10) {
        fprintf(stderr, "HDR output requires bitdepth 10 for the Apple encoder\n");
        return -1;
    }
    return 0;
}

static int adjusted_bitrate_kbps(const apple_output_config *cfg)
{
    if (cfg->bitrate_kbps <= 0)
        return 0;

    int percent = 100;
    if (cfg->bitrate_kbps > 35000) {
        percent = 125;
    } else if (cfg->bitrate_kbps > 25000) {
        percent = 120;
    } else if (cfg->bitrate_kbps > 10000) {
        percent = 110;
    }

    int64_t adjusted = ((int64_t)cfg->bitrate_kbps * percent + 99) / 100;
    if (cfg->maxrate_kbps > 0 && adjusted > cfg->maxrate_kbps)
        adjusted = cfg->maxrate_kbps;
    if (adjusted > MAX_VIDEO_BITRATE_KBPS)
        adjusted = MAX_VIDEO_BITRATE_KBPS;
    return (int)adjusted;
}

static void free_outputs(apple_params *p)
{
    if (!p->outputs)
        return;
    for (int i = 0; i < p->output_count; i++) {
        free(p->outputs[i].out_file);
        free(p->outputs[i].profile);
    }
    free(p->outputs);
    p->outputs = NULL;
    p->output_count = 0;
}

static int add_single_output(apple_params *p)
{
    p->output_count = 1;
    p->outputs = (apple_output_config *)calloc(1, sizeof(apple_output_config));
    if (!p->outputs) {
        fprintf(stderr, "Out of memory\n");
        return -1;
    }
    apple_output_config *cfg = &p->outputs[0];
    cfg->out_file = strdup(p->out_file);
    cfg->profile = p->profile ? strdup(p->profile) : NULL;
    cfg->bitrate_kbps = p->bitrate_kbps;
    cfg->maxrate_kbps = p->maxrate_kbps;
    cfg->width = p->width;
    cfg->height = p->height;
    cfg->level = p->level;
    cfg->bitdepth = p->bitdepth;
    return validate_output_config(p, cfg);
}

static int load_abr_profile(apple_params *p)
{
    NSString *profile_path = [NSString stringWithUTF8String:p->abr_profile_file];
    NSError *error = nil;
    NSData *data = [NSData dataWithContentsOfFile:profile_path options:0 error:&error];
    if (!data) {
        fprintf(stderr, "Could not read ABR profile %s: %s\n",
                p->abr_profile_file, error.localizedDescription.UTF8String);
        return -1;
    }

    id root = [NSJSONSerialization JSONObjectWithData:data options:0 error:&error];
    if (![root isKindOfClass:[NSDictionary class]]) {
        fprintf(stderr, "ABR profile is not a JSON object: %s\n",
                error.localizedDescription.UTF8String ?: "invalid JSON");
        return -1;
    }

    NSDictionary *profile = (NSDictionary *)root;
    id no_upscale = profile[@"no_upscale"];
    if ([no_upscale respondsToSelector:@selector(boolValue)])
        p->no_upscale = [no_upscale boolValue] ? 1 : 0;

    NSDictionary *video_segment = nil;
    id segment_specs = profile[@"segment_specs"];
    if ([segment_specs isKindOfClass:[NSDictionary class]]) {
        id video = [(NSDictionary *)segment_specs objectForKey:@"video"];
        if ([video isKindOfClass:[NSDictionary class]])
            video_segment = (NSDictionary *)video;
    }
    int bitdepth = p->bitdepth;
    id bit_depth_value = video_segment[@"bit_depth"];
    if ([bit_depth_value isKindOfClass:[NSNumber class]])
        bitdepth = [(NSNumber *)bit_depth_value intValue];

    NSMutableArray<NSDictionary *> *video_rungs = [NSMutableArray array];
    id ladder_specs = profile[@"ladder_specs"];
    if ([ladder_specs isKindOfClass:[NSDictionary class]]) {
        for (id key in (NSDictionary *)ladder_specs) {
            id ladder = [(NSDictionary *)ladder_specs objectForKey:key];
            if (![ladder isKindOfClass:[NSDictionary class]])
                continue;
            id rung_specs = [(NSDictionary *)ladder objectForKey:@"rung_specs"];
            if (![rung_specs isKindOfClass:[NSArray class]])
                continue;
            for (id rung in (NSArray *)rung_specs) {
                if (![rung isKindOfClass:[NSDictionary class]])
                    continue;
                id media_type = [(NSDictionary *)rung objectForKey:@"media_type"];
                if ([media_type isKindOfClass:[NSString class]] &&
                    [(NSString *)media_type isEqualToString:@"video"]) {
                    [video_rungs addObject:(NSDictionary *)rung];
                }
            }
        }
    }

    if (video_rungs.count == 0) {
        fprintf(stderr, "ABR profile contains no video rung_specs\n");
        return -1;
    }

    [video_rungs sortUsingComparator:^NSComparisonResult(NSDictionary *a, NSDictionary *b) {
        int ah = [a[@"height"] intValue];
        int bh = [b[@"height"] intValue];
        if (ah != bh)
            return ah > bh ? NSOrderedAscending : NSOrderedDescending;
        long long ab = [a[@"bit_rate"] longLongValue];
        long long bb = [b[@"bit_rate"] longLongValue];
        if (ab == bb)
            return NSOrderedSame;
        return ab > bb ? NSOrderedAscending : NSOrderedDescending;
    }];

    p->output_count = (int)video_rungs.count;
    p->outputs = (apple_output_config *)calloc((size_t)p->output_count, sizeof(apple_output_config));
    if (!p->outputs) {
        fprintf(stderr, "Out of memory\n");
        return -1;
    }

    NSString *base_output = [NSString stringWithUTF8String:p->out_file];
    for (int i = 0; i < p->output_count; i++) {
        NSDictionary *rung = video_rungs[(NSUInteger)i];
        apple_output_config *cfg = &p->outputs[i];
        long long bit_rate_bps = [rung[@"bit_rate"] longLongValue];
        cfg->bitrate_kbps = (int)((bit_rate_bps + 999) / 1000);
        cfg->maxrate_kbps = p->maxrate_kbps;
        cfg->width = [rung[@"width"] intValue];
        cfg->height = [rung[@"height"] intValue];
        cfg->level = level_from_json(rung[@"level"]);
        if (cfg->level == 0)
            cfg->level = p->level;
        cfg->bitdepth = bitdepth;

        id rung_profile = rung[@"profile"];
        if ([rung_profile isKindOfClass:[NSString class]]) {
            cfg->profile = strdup([(NSString *)rung_profile UTF8String]);
        } else if (p->profile) {
            cfg->profile = strdup(p->profile);
        }

        NSString *out_path = abr_output_path(base_output, cfg, i);
        cfg->out_file = strdup_nsstring(out_path);

        if (cfg->bitrate_kbps <= 0 || cfg->width <= 0 || cfg->height <= 0) {
            fprintf(stderr, "Invalid video rung in ABR profile: width=%d height=%d bitrate=%d kbps\n",
                    cfg->width, cfg->height, cfg->bitrate_kbps);
            return -1;
        }
        if (validate_output_config(p, cfg) < 0)
            return -1;
    }

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
        } else if (!strcmp(opt, "-quality") && argi < argc) {
            if (parse_quality_double(argv[argi], &p->quality) < 0) {
                fprintf(stderr, "Invalid quality '%s', expected a value from 0.0 to 1.0\n", argv[argi]);
                return -1;
            }
            argi++;
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
            p->level = parse_level_value(argv[argi++]);
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
        } else if (!strcmp(opt, "-abr-profile") && argi < argc) {
            p->abr_profile_file = argv[argi++];
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
    if (p->abr_profile_file)
        return load_abr_profile(p);
    return add_single_output(p);
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

static NSString *format_description_string_extension(CFDictionaryRef extensions,
                                                     CFStringRef key)
{
    if (!extensions)
        return nil;
    CFTypeRef value = CFDictionaryGetValue(extensions, key);
    if (!value || CFGetTypeID(value) != CFStringGetTypeID())
        return nil;
    return (__bridge NSString *)value;
}

static NSDictionary<NSString *, NSString *> *track_color_properties(AVAssetTrack *track)
{
    NSArray *format_descriptions = track.formatDescriptions;
    if (format_descriptions.count == 0)
        return nil;

    CMFormatDescriptionRef format =
        (__bridge CMFormatDescriptionRef)format_descriptions.firstObject;
    CFDictionaryRef extensions = CMFormatDescriptionGetExtensions(format);
    NSString *primaries = format_description_string_extension(
        extensions, kCMFormatDescriptionExtension_ColorPrimaries);
    NSString *transfer = format_description_string_extension(
        extensions, kCMFormatDescriptionExtension_TransferFunction);
    NSString *matrix = format_description_string_extension(
        extensions, kCMFormatDescriptionExtension_YCbCrMatrix);

    NSMutableDictionary<NSString *, NSString *> *properties =
        [NSMutableDictionary dictionaryWithCapacity:3];
    if (primaries)
        properties[AVVideoColorPrimariesKey] = primaries;
    if (transfer)
        properties[AVVideoTransferFunctionKey] = transfer;
    if (matrix)
        properties[AVVideoYCbCrMatrixKey] = matrix;
    return properties.count > 0 ? [properties copy] : nil;
}

static NSDictionary<NSString *, NSString *> *resolve_sdr_color_properties(
    AVAssetTrack *left_track,
    AVAssetTrack *right_track,
    BOOL *compatible)
{
    NSDictionary<NSString *, NSString *> *left = track_color_properties(left_track);
    NSDictionary<NSString *, NSString *> *right = track_color_properties(right_track);
    NSArray<NSString *> *keys = @[
        AVVideoColorPrimariesKey,
        AVVideoTransferFunctionKey,
        AVVideoYCbCrMatrixKey,
    ];
    NSMutableDictionary<NSString *, NSString *> *resolved =
        [NSMutableDictionary dictionaryWithCapacity:3];

    *compatible = YES;
    for (NSString *key in keys) {
        NSString *left_value = left[key];
        NSString *right_value = right[key];
        if (left_value && right_value && ![left_value isEqualToString:right_value]) {
            fprintf(stderr, "Left/right SDR color metadata differs for %s: %s vs %s\n",
                    key.UTF8String, left_value.UTF8String, right_value.UTF8String);
            *compatible = NO;
            return nil;
        }
        NSString *value = left_value ?: right_value;
        if (value)
            resolved[key] = value;
    }

    if (resolved.count == 0)
        return nil;
    if (resolved.count != keys.count) {
        fprintf(stderr,
                "Warning: SDR source color metadata is incomplete; not writing color signaling\n");
        return nil;
    }
    if (left.count != right.count) {
        fprintf(stderr,
                "Warning: only one eye has complete SDR color metadata; using it for both views\n");
    }
    return [resolved copy];
}

static BOOL copy_to_pool(CVPixelBufferPoolRef pool,
                         VTPixelTransferSessionRef transfer,
                         CVImageBufferRef src,
                         NSDictionary<NSString *, NSString *> *color_properties,
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

    if (color_properties) {
        CVBufferSetAttachment(dst, kCVImageBufferColorPrimariesKey,
                              (__bridge CFStringRef)color_properties[AVVideoColorPrimariesKey],
                              kCVAttachmentMode_ShouldPropagate);
        CVBufferSetAttachment(dst, kCVImageBufferTransferFunctionKey,
                              (__bridge CFStringRef)color_properties[AVVideoTransferFunctionKey],
                              kCVAttachmentMode_ShouldPropagate);
        CVBufferSetAttachment(dst, kCVImageBufferYCbCrMatrixKey,
                              (__bridge CFStringRef)color_properties[AVVideoYCbCrMatrixKey],
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

static NSString *profile_level_for_output(const apple_output_config *cfg)
{
    return cfg->bitdepth == 10 || (cfg->profile && !strcmp(cfg->profile, "main10")) ?
        (__bridge NSString *)kVTProfileLevel_HEVC_Main10_AutoLevel :
        (__bridge NSString *)kVTProfileLevel_HEVC_Main_AutoLevel;
}

static BOOL compute_output_size(const apple_output_config *cfg,
                                int src_w,
                                int src_h,
                                int *out_w,
                                int *out_h)
{
    int w = cfg->width;
    int h = cfg->height;
    if (w <= 0 && h <= 0) {
        w = src_w;
        h = src_h;
    } else if (w > 0 && h <= 0) {
        h = (int)llround((double)w * (double)src_h / (double)src_w);
    } else if (h > 0 && w <= 0) {
        w = (int)llround((double)h * (double)src_w / (double)src_h);
    }
    if ((w & 1) || (h & 1)) {
        fprintf(stderr, "Output dimensions must be even for 4:2:0 HEVC (got %dx%d)\n", w, h);
        return NO;
    }
    *out_w = w;
    *out_h = h;
    return YES;
}

static AppleOutputContext *create_output_context(const apple_params *p,
                                                 apple_output_config *cfg,
                                                 int src_w,
                                                 int src_h,
                                                 NSDictionary<NSString *, NSString *> *sdr_color_properties)
{
    AppleOutputContext *ctx = [[AppleOutputContext alloc] init];
    ctx.config = cfg;
    ctx.outPath = [NSString stringWithUTF8String:cfg->out_file];
    ctx.fileType = file_type_for_output(ctx.outPath);

    int out_w = 0;
    int out_h = 0;
    if (!compute_output_size(cfg, src_w, src_h, &out_w, &out_h))
        return nil;
    ctx.outWidth = out_w;
    ctx.outHeight = out_h;

    NSURL *out_url = [NSURL fileURLWithPath:ctx.outPath];
    [[NSFileManager defaultManager] removeItemAtURL:out_url error:nil];

    NSError *error = nil;
    ctx.writer = [[AVAssetWriter alloc] initWithURL:out_url fileType:ctx.fileType error:&error];
    if (!ctx.writer) {
        fprintf(stderr, "Could not create writer for %s: %s\n",
                cfg->out_file, error.localizedDescription.UTF8String);
        return nil;
    }

    NSMutableDictionary *compression = [NSMutableDictionary dictionary];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_MVHEVCVideoLayerIDs] = @[@0, @1];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_MVHEVCViewIDs] = @[@0, @1];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_MVHEVCLeftAndRightViewIDs] = @[@0, @1];
    compression[(__bridge NSString *)kVTCompressionPropertyKey_HasLeftStereoEyeView] = @YES;
    compression[(__bridge NSString *)kVTCompressionPropertyKey_HasRightStereoEyeView] = @YES;
    compression[AVVideoProfileLevelKey] = profile_level_for_output(cfg);

    int encoder_bitrate_kbps = adjusted_bitrate_kbps(cfg);
    if (encoder_bitrate_kbps > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_AverageBitRate] = @((int64_t)encoder_bitrate_kbps * 1000);
    if (cfg->maxrate_kbps > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_DataRateLimits] = @[@((cfg->maxrate_kbps * 1000) / 8), @1];
    if (p->quality >= 0.0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_Quality] = @(p->quality);
    if (p->keyint > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_MaxKeyFrameInterval] = @(p->keyint);
    if (p->bframes >= 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_AllowFrameReordering] = p->bframes == 0 ? @NO : @YES;
    if (p->fps_num > 0 && p->fps_den > 0)
        compression[(__bridge NSString *)kVTCompressionPropertyKey_ExpectedFrameRate] = @((double)p->fps_num / (double)p->fps_den);

    NSMutableDictionary *output_settings = [@{
        AVVideoCodecKey: AVVideoCodecTypeHEVC,
        AVVideoWidthKey: @(ctx.outWidth),
        AVVideoHeightKey: @(ctx.outHeight),
        AVVideoCompressionPropertiesKey: compression,
    } mutableCopy];

    if (p->hdr) {
        ctx.colorProperties = @{
            AVVideoColorPrimariesKey: AVVideoColorPrimaries_ITU_R_2020,
            AVVideoTransferFunctionKey: AVVideoTransferFunction_SMPTE_ST_2084_PQ,
            AVVideoYCbCrMatrixKey: AVVideoYCbCrMatrix_ITU_R_2020,
        };
    } else {
        ctx.colorProperties = sdr_color_properties;
    }
    if (ctx.colorProperties) {
        output_settings[AVVideoColorPropertiesKey] = ctx.colorProperties;
        compression[(__bridge NSString *)kVTCompressionPropertyKey_ColorPrimaries] =
            ctx.colorProperties[AVVideoColorPrimariesKey];
        compression[(__bridge NSString *)kVTCompressionPropertyKey_TransferFunction] =
            ctx.colorProperties[AVVideoTransferFunctionKey];
        compression[(__bridge NSString *)kVTCompressionPropertyKey_YCbCrMatrix] =
            ctx.colorProperties[AVVideoYCbCrMatrixKey];
    }
    if (p->hdr) {
        compression[(__bridge NSString *)kVTCompressionPropertyKey_HDRMetadataInsertionMode] =
            (__bridge NSString *)kVTHDRMetadataInsertionMode_Auto;

        NSData *mdcv = master_display_data(p->master_display);
        NSData *clli = content_light_data(p->max_cll);
        if (mdcv)
            compression[(__bridge NSString *)kVTCompressionPropertyKey_MasteringDisplayColorVolume] = mdcv;
        if (clli)
            compression[(__bridge NSString *)kVTCompressionPropertyKey_ContentLightLevelInfo] = clli;
    }

    if (![ctx.writer canApplyOutputSettings:output_settings forMediaType:AVMediaTypeVideo]) {
        fprintf(stderr, "Writer cannot apply requested MV-HEVC output settings for %s\n", cfg->out_file);
        return nil;
    }

    ctx.writerInput = [[AVAssetWriterInput alloc] initWithMediaType:AVMediaTypeVideo outputSettings:output_settings];
    ctx.writerInput.expectsMediaDataInRealTime = NO;

    OSType writer_pixel_format = cfg->bitdepth == 10 ?
        kCVPixelFormatType_420YpCbCr10BiPlanarVideoRange :
        kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;
    NSDictionary *source_attrs = @{
        (__bridge NSString *)kCVPixelBufferPixelFormatTypeKey: @(writer_pixel_format),
        (__bridge NSString *)kCVPixelBufferWidthKey: @(ctx.outWidth),
        (__bridge NSString *)kCVPixelBufferHeightKey: @(ctx.outHeight),
        (__bridge NSString *)kCVPixelBufferIOSurfacePropertiesKey: @{},
    };
    ctx.adaptor = [[AVAssetWriterInputTaggedPixelBufferGroupAdaptor alloc] initWithAssetWriterInput:ctx.writerInput
                                                                        sourcePixelBufferAttributes:source_attrs];

    if (![ctx.writer canAddInput:ctx.writerInput]) {
        fprintf(stderr, "Could not add writer input for %s\n", cfg->out_file);
        return nil;
    }
    [ctx.writer addInput:ctx.writerInput];

    VTPixelTransferSessionRef transfer = NULL;
    OSStatus st = VTPixelTransferSessionCreate(kCFAllocatorDefault, &transfer);
    if (st != noErr) {
        fprintf(stderr, "VTPixelTransferSessionCreate failed for %s: %d\n", cfg->out_file, (int)st);
        return nil;
    }
    ctx.transfer = transfer;
    VTSessionSetProperty(ctx.transfer, kVTPixelTransferPropertyKey_ScalingMode, kVTScalingMode_Normal);
    if (ctx.colorProperties) {
        VTSessionSetProperty(ctx.transfer, kVTPixelTransferPropertyKey_DestinationColorPrimaries,
                             (__bridge CFStringRef)ctx.colorProperties[AVVideoColorPrimariesKey]);
        VTSessionSetProperty(ctx.transfer, kVTPixelTransferPropertyKey_DestinationTransferFunction,
                             (__bridge CFStringRef)ctx.colorProperties[AVVideoTransferFunctionKey]);
        VTSessionSetProperty(ctx.transfer, kVTPixelTransferPropertyKey_DestinationYCbCrMatrix,
                             (__bridge CFStringRef)ctx.colorProperties[AVVideoYCbCrMatrixKey]);
    }

    return ctx;
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
    NSURL *left_url = [NSURL fileURLWithPath:left_path];
    NSURL *right_url = [NSURL fileURLWithPath:right_path];

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

    NSDictionary<NSString *, NSString *> *sdr_color_properties = nil;
    if (!p->hdr) {
        BOOL compatible = YES;
        sdr_color_properties = resolve_sdr_color_properties(
            left_track, right_track, &compatible);
        if (!compatible)
            return 1;
    }

    int reader_bitdepth = 8;
    for (int i = 0; i < p->output_count; i++) {
        if (p->outputs[i].bitdepth > reader_bitdepth)
            reader_bitdepth = p->outputs[i].bitdepth;
    }
    OSType reader_pixel_format = reader_bitdepth == 10 ?
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

    NSMutableArray<AppleOutputContext *> *outputs = [NSMutableArray arrayWithCapacity:(NSUInteger)p->output_count];
    for (int i = 0; i < p->output_count; i++) {
        if (p->no_upscale &&
            ((p->outputs[i].width > 0 && p->outputs[i].width > src_w) ||
             (p->outputs[i].height > 0 && p->outputs[i].height > src_h))) {
            fprintf(stderr, "Skipping ABR rung above source size due to no_upscale: %dx%d -> %s\n",
                    p->outputs[i].width, p->outputs[i].height, p->outputs[i].out_file);
            continue;
        }
        AppleOutputContext *ctx = create_output_context(
            p, &p->outputs[i], src_w, src_h, sdr_color_properties);
        if (!ctx)
            return 1;
        [outputs addObject:ctx];
    }
    if (outputs.count == 0) {
        fprintf(stderr, "No output rungs remain after applying ABR profile constraints\n");
        return 1;
    }

    if (![left_reader startReading] || ![right_reader startReading]) {
        fprintf(stderr, "Could not start readers: left=%s right=%s\n",
                left_reader.error.localizedDescription.UTF8String ?: "ok",
                right_reader.error.localizedDescription.UTF8String ?: "ok");
        return 1;
    }
    for (AppleOutputContext *ctx in outputs) {
        if (![ctx.writer startWriting]) {
            fprintf(stderr, "Could not start writer for %s: %s\n",
                    ctx.config->out_file, ctx.writer.error.localizedDescription.UTF8String);
            return 1;
        }
        [ctx.writer startSessionAtSourceTime:kCMTimeZero];
        ctx.pool = ctx.adaptor.pixelBufferPool;
        if (!ctx.pool) {
            fprintf(stderr, "Writer adaptor did not provide a pixel buffer pool for %s\n",
                    ctx.config->out_file);
            return 1;
        }
    }

    fprintf(stderr, "Apple MV-HEVC encoder configuration:\n");
    fprintf(stderr, "  Input:       %dx%d\n", src_w, src_h);
    fprintf(stderr, "  Outputs:     %lu\n", (unsigned long)outputs.count);
    for (AppleOutputContext *ctx in outputs) {
        int encoder_bitrate_kbps = adjusted_bitrate_kbps(ctx.config);
        fprintf(stderr, "    %dx%d %s requested=%d kbps adjusted=%d kbps bitdepth=%d profile=%s level=%d -> %s\n",
                ctx.outWidth, ctx.outHeight, ctx.fileType.UTF8String,
                ctx.config->bitrate_kbps, encoder_bitrate_kbps,
                ctx.config->bitdepth,
                ctx.config->profile ? ctx.config->profile : "(auto)",
                ctx.config->level,
                ctx.config->out_file);
    }
    if (p->fps_num > 0)
        fprintf(stderr, "  FPS:         %d/%d\n", p->fps_num, p->fps_den);
    if (p->duration_seconds > 0.0)
        fprintf(stderr, "  Duration:    %.3f seconds\n", p->duration_seconds);
    if (p->quality >= 0.0)
        fprintf(stderr, "  Quality:     %.3f\n", p->quality);
    if (p->hdr)
        fprintf(stderr, "  HDR10:       BT.2020/PQ + optional MDCV/CLLI\n");
    else if (sdr_color_properties)
        fprintf(stderr, "  SDR color:   primaries=%s transfer=%s matrix=%s\n",
                [sdr_color_properties[AVVideoColorPrimariesKey] UTF8String],
                [sdr_color_properties[AVVideoTransferFunctionKey] UTF8String],
                [sdr_color_properties[AVVideoYCbCrMatrixKey] UTF8String]);
    if (p->abr_profile_file)
        fprintf(stderr, "  ABR profile: %s\n", p->abr_profile_file);
    if (p->no_upscale)
        fprintf(stderr, "  No upscale:  true\n");
    if (p->saw_x265_only)
        fprintf(stderr, "  Note:        x265-only options were accepted but ignored by the Apple encoder\n");

    int64_t frame_count = 0;
    CMTime first_pts = kCMTimeInvalid;
    CMTime duration_limit = p->duration_seconds > 0.0 ?
        CMTimeMakeWithSeconds(p->duration_seconds, 1000000) :
        kCMTimeInvalid;
    BOOL ok = YES;

    for (;;) {
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

        for (AppleOutputContext *ctx in outputs) {
            if (!wait_until_ready(ctx.writerInput, ctx.writer)) {
                fprintf(stderr, "Writer failed while waiting for %s: %s\n",
                        ctx.config->out_file,
                        ctx.writer.error.localizedDescription.UTF8String ?: "unknown writer error");
                ok = NO;
                break;
            }

            CVPixelBufferRef left_dst = NULL;
            CVPixelBufferRef right_dst = NULL;
            if (!copy_to_pool(ctx.pool, ctx.transfer, left_src, ctx.colorProperties, &left_dst) ||
                !copy_to_pool(ctx.pool, ctx.transfer, right_src, ctx.colorProperties, &right_dst)) {
                if (left_dst)
                    CVPixelBufferRelease(left_dst);
                if (right_dst)
                    CVPixelBufferRelease(right_dst);
                ok = NO;
                break;
            }

            CMTaggedBufferGroupRef group = create_tagged_group(left_dst, right_dst);
            if (!group) {
                CVPixelBufferRelease(left_dst);
                CVPixelBufferRelease(right_dst);
                ok = NO;
                break;
            }

            BOOL appended = [ctx.adaptor appendTaggedPixelBufferGroup:group withPresentationTime:pts];
            CFRelease(group);
            CVPixelBufferRelease(left_dst);
            CVPixelBufferRelease(right_dst);

            if (!appended) {
                fprintf(stderr, "appendTaggedPixelBufferGroup failed for %s: %s\n",
                        ctx.config->out_file,
                        ctx.writer.error.localizedDescription.UTF8String ?: "unknown writer error");
                ok = NO;
                break;
            }
            ctx.frameCount++;
        }

        CFRelease(left_sample);
        CFRelease(right_sample);
        if (!ok)
            break;

        frame_count++;
        if (frame_count % 100 == 0)
            fprintf(stderr, "Encoded %lld source frames across %lu outputs\n",
                    (long long)frame_count, (unsigned long)outputs.count);
    }

    for (AppleOutputContext *ctx in outputs)
        [ctx.writerInput markAsFinished];
    if (!ok) {
        for (AppleOutputContext *ctx in outputs)
            [ctx.writer cancelWriting];
        return 1;
    }

    for (AppleOutputContext *ctx in outputs) {
        __block BOOL finished = NO;
        __block BOOL finish_ok = NO;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        AVAssetWriter *writer = ctx.writer;
        [writer finishWritingWithCompletionHandler:^{
            finish_ok = writer.status == AVAssetWriterStatusCompleted;
            finished = YES;
            dispatch_semaphore_signal(sem);
        }];
        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

        if (!finished || !finish_ok) {
            fprintf(stderr, "finishWriting failed for %s: %s\n",
                    ctx.config->out_file,
                    writer.error.localizedDescription.UTF8String ?: "unknown writer error");
            return 1;
        }
    }

    fprintf(stderr, "Encoded %lld source frames\n", (long long)frame_count);
    for (AppleOutputContext *ctx in outputs)
        fprintf(stderr, "Output: %s (%lld frames)\n",
                ctx.config->out_file, (long long)ctx.frameCount);
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
            free_outputs(&params);
            return 1;
        }

        int rc = 1;
        @try {
            rc = encode_apple(&params);
        } @catch (NSException *exception) {
            fprintf(stderr, "Objective-C exception: %s: %s\n",
                    exception.name.UTF8String,
                    exception.reason.UTF8String ?: "");
            rc = 1;
        }
        free_outputs(&params);
        return rc;
    }
}
