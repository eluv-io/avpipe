/*
 * Simple test producer to push HDR10+ JSON to avpipe using avpipe_set_hdr10plus.
 * Usage:
 *   echo "1000 {\"master\":\"...\"}" | ./hdr10pp_producer
 * Lines are: <pts> <json>
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

/* Forward-declare the minimal HDR10+ API to avoid pulling in FFmpeg headers
 * (avpipe.h includes avpipe_xc.h which depends on libav* headers). This
 * producer only needs the `avpipe_set_hdr10plus` symbol at link time. */
int avpipe_set_hdr10plus(int64_t pts, const char *json, int json_len);

int main(int argc, char **argv) {
    char *line = NULL;
    size_t len = 0;
    size_t read;

    while ((read = getline(&line, &len, stdin)) != -1) {
        if (read <= 1) continue;
        // Trim newline
        if (line[read-1] == '\n') line[read-1] = '\0';

        // Find first space
        char *sp = strchr(line, ' ');
        if (!sp) continue;
        *sp = '\0';
        const char *pts_str = line;
        const char *json = sp + 1;
        long long pts = atoll(pts_str);

        if (avpipe_set_hdr10plus((int64_t)pts, json, (int)strlen(json)) != 0) {
            fprintf(stderr, "avpipe_set_hdr10plus failed for pts=%lld\n", pts);
        } else {
            printf("Set HDR10+ for PTS=%lld\n", pts);
        }
    }

    free(line);
    return 0;
}
