#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

/* Forward-declare minimal HDR10+ API to avoid pulling heavy FFmpeg headers. */
int avpipe_set_hdr10plus(int64_t pts, const char *json, int json_len);
char *avpipe_get_hdr10plus(int64_t pts);
int avpipe_hdr10plus_set_tolerance(int64_t tolerance_pts);
int avpipe_hdr10plus_set_ttl(int ttl_seconds);
int avpipe_hdr10plus_set_capacity(int max_entries);

static void fail(const char *msg) {
    fprintf(stderr, "FAIL: %s\n", msg);
    exit(1);
}

static void ok(const char *msg) {
    fprintf(stdout, "OK: %s\n", msg);
}

int main(void) {
    char *j;

    /* Setup: TTL large, capacity small for LRU test */
    avpipe_hdr10plus_set_ttl(3600);
    avpipe_hdr10plus_set_capacity(3);
    avpipe_hdr10plus_set_tolerance(0);

    /* Insert 10,20,30 */
    if (avpipe_set_hdr10plus(10, "a", 1) != 0) fail("set 10");
    if (avpipe_set_hdr10plus(20, "b", 1) != 0) fail("set 20");
    if (avpipe_set_hdr10plus(30, "c", 1) != 0) fail("set 30");

    /* Replace 10 -> moves 10 to MRU */
    if (avpipe_set_hdr10plus(10, "a2", 2) != 0) fail("replace 10");

     /* Replace 10 -> moves 10 to MRU */
     /* (previously had an immediate-get debug check here; removed)
         Keep the replace so LRU ordering for subsequent inserts is exercised. */

    /* Insert 40 -> should evict LRU (which is 20) */
    if (avpipe_set_hdr10plus(40, "d", 1) != 0) fail("set 40");

    j = avpipe_get_hdr10plus(20);
    if (j != NULL) {
        fprintf(stderr, "Unexpectedly found metadata for 20: %s\n", j);
        free(j);
        fail("LRU eviction failed (20 should be evicted)");
    } else {
        ok("LRU eviction evicted oldest (20)");
    }

    /* Now check remaining entries: 10,30,40 exist */
    j = avpipe_get_hdr10plus(10);
    if (!j) fail("expected 10");
    if (strcmp(j, "a2") != 0) fail("10 value mismatch");
    free(j);
    ok("got 10 correct");

    j = avpipe_get_hdr10plus(30);
    if (!j) fail("expected 30");
    if (strcmp(j, "c") != 0) fail("30 value mismatch");
    free(j);
    ok("got 30 correct");

    j = avpipe_get_hdr10plus(40);
    if (!j) fail("expected 40");
    if (strcmp(j, "d") != 0) fail("40 value mismatch");
    free(j);
    ok("got 40 correct");

    /* Test tolerance-based nearest PTS */
    avpipe_hdr10plus_set_capacity(1000);
    avpipe_hdr10plus_set_ttl(3600);
    avpipe_hdr10plus_set_tolerance(2);

    if (avpipe_set_hdr10plus(100, "x", 1) != 0) fail("set 100");
    /* get at 102 should match 100 */
    j = avpipe_get_hdr10plus(102);
    if (!j) fail("expected nearest match for 102");
    if (strcmp(j, "x") != 0) fail("nearest match value mismatch");
    free(j);
    ok("nearest-PTS tolerance match succeeded");

    printf("ALL TESTS PASSED\n");
    return 0;
}
