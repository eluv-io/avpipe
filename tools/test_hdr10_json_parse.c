/*
 * Simple test for HDR10+ JSON parser
 */
#include <stdio.h>
#include <stdlib.h>
#include "hdr10plus_json.h"
#include <libavutil/hdr_dynamic_metadata.h>

int main() {
    const char *json = "{\"NumberOfWindows\":1,\"TargetedSystemDisplayMaximumLuminance\":1000,\"MaxScl\":[100000,100000,100000],\"AverageRGB\":50000,\"DistributionIndex\":[1,5,10,25,50,75,90,95,99],\"DistributionValues\":[10000,20000,30000,40000,50000,60000,70000,80000,90000]}";

    printf("Testing HDR10+ JSON parser with: %s\n", json);

    AVDynamicHDRPlus *meta = NULL;
    AVBufferRef *buf = NULL;

    printf("Calling parser...\n");
    int ret = avpipe_hdr10plus_json_to_metadata(json, &meta, &buf);

    printf("Parser returned: %d\n", ret);
    if (ret == 0) {
        printf("Success! num_windows=%d\n", meta->num_windows);
        printf("TSDML=%d/%d\n", meta->targeted_system_display_maximum_luminance.num,
               meta->targeted_system_display_maximum_luminance.den);
        if (buf) {
            printf("Buffer size: %zu\n", buf->size);
            av_buffer_unref(&buf);
        } else {
            printf("No buffer created (conversion disabled)\n");
        }
        av_free(meta);
    } else {
        printf("Failed!\n");
    }

    return 0;
}
