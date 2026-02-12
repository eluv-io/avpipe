/* HDR10+ simple store implementation for libavpipe */
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <pthread.h>
#include <time.h>
#include <stdint.h>
#include <limits.h>
#include "../../avpipe.h"

typedef struct hdr_entry_t {
    int64_t pts;
    char *json;
    int len;
    int64_t inserted_ts; /* seconds since epoch */
    struct hdr_entry_t *prev; /* LRU prev */
    struct hdr_entry_t *next; /* LRU next */
    struct hdr_entry_t *hnext; /* hash bucket next */
} hdr_entry_t;

/* LRU list head = most recently used, tail = least recently used */
static hdr_entry_t *hdr_lru_head = NULL;
static hdr_entry_t *hdr_lru_tail = NULL;
static pthread_mutex_t hdr_store_mutex = PTHREAD_MUTEX_INITIALIZER;
static int64_t hdr10plus_tolerance_pts = 0; /* match within +/- pts */
static int hdr10plus_ttl_seconds = 30; /* entry time-to-live seconds */
static int hdr10plus_max_entries = 10000; /* max entries before eviction */
static int hdr10plus_count = 0;
/* Simple hash table to index entries by exact PTS for fast lookup. */
#define HDR_HASH_SIZE 16384
static hdr_entry_t *hdr_buckets[HDR_HASH_SIZE];

static inline unsigned int hdr_hash_idx(int64_t pts) {
    return (unsigned int)((uint64_t)pts % HDR_HASH_SIZE);
}

/* Unlink an entry from the hash bucket chain (caller must hold mutex) */
static void hdr_unlink_from_bucket(hdr_entry_t *e) {
    unsigned int idx = hdr_hash_idx(e->pts);
    hdr_entry_t *cur = hdr_buckets[idx];
    hdr_entry_t *prev = NULL;
    while (cur) {
        if (cur == e) {
            if (prev) prev->hnext = cur->hnext;
            else hdr_buckets[idx] = cur->hnext;
            return;
        }
        prev = cur;
        cur = cur->hnext;
    }
}

/* Link an entry into the hash bucket chain (caller must hold mutex) */
static void hdr_link_into_bucket(hdr_entry_t *e) {
    unsigned int idx = hdr_hash_idx(e->pts);
    e->hnext = hdr_buckets[idx];
    hdr_buckets[idx] = e;
}

int
avpipe_set_hdr10plus(
    int64_t pts,
    const char *json,
    int json_len)
{
    if (!json || json_len <= 0)
        return -1;

    hdr_entry_t *e = (hdr_entry_t *)calloc(1, sizeof(hdr_entry_t));
    if (!e)
        return -1;
    e->pts = pts;
    e->json = (char *)malloc(json_len + 1);
    if (!e->json) {
        free(e);
        return -1;
    }
    memcpy(e->json, json, json_len);
    e->json[json_len] = '\0';
    e->len = json_len;

    pthread_mutex_lock(&hdr_store_mutex);
    /* prune expired entries first */
    int64_t now = (int64_t)time(NULL);
    hdr_entry_t *cur = hdr_lru_head;
    while (cur) {
        hdr_entry_t *nxt = cur->next;
        if (hdr10plus_ttl_seconds > 0 && now - cur->inserted_ts > hdr10plus_ttl_seconds) {
            /* unlink cur from LRU */
            if (cur->prev) cur->prev->next = cur->next;
            else hdr_lru_head = cur->next;
            if (cur->next) cur->next->prev = cur->prev;
            else hdr_lru_tail = cur->prev;
            /* unlink from hash */
            hdr_unlink_from_bucket(cur);
            free(cur->json);
            free(cur);
            hdr10plus_count--;
        }
        cur = nxt;
    }

    /* If an entry with same PTS exists, replace its JSON and move to head. Use hash for fast find. */
    {
        unsigned int idx = hdr_hash_idx(pts);
        hdr_entry_t *hcur = hdr_buckets[idx];
        while (hcur) {
            if (hcur->pts == pts) {
                char *newjson = (char *)realloc(hcur->json, json_len + 1);
                if (newjson) {
                    hcur->json = newjson;
                    memcpy(hcur->json, json, json_len);
                    hcur->json[json_len] = '\0';
                    hcur->len = json_len;
                }
                hcur->inserted_ts = now;
                /* move to head if not already */
                if (hcur != hdr_lru_head) {
                    /* unlink hcur */
                    if (hcur->prev) hcur->prev->next = hcur->next;
                    if (hcur->next) hcur->next->prev = hcur->prev;
                    if (hcur == hdr_lru_tail) hdr_lru_tail = hcur->prev;
                    /* insert at head */
                    hcur->prev = NULL;
                    hcur->next = hdr_lru_head;
                    if (hdr_lru_head) hdr_lru_head->prev = hcur;
                    hdr_lru_head = hcur;
                }
                pthread_mutex_unlock(&hdr_store_mutex);
                free(e->json);
                free(e);
                return 0;
            }
            hcur = hcur->hnext;
        }
    }

    /* Evict least-recently-used (tail) while over capacity */
    while (hdr10plus_max_entries > 0 && hdr10plus_count >= hdr10plus_max_entries) {
        if (!hdr_lru_tail) break;
        hdr_entry_t *old = hdr_lru_tail;
        if (old->prev) old->prev->next = NULL;
        hdr_lru_tail = old->prev;
        if (!hdr_lru_tail) hdr_lru_head = NULL;
        /* unlink from bucket */
        hdr_unlink_from_bucket(old);
        free(old->json);
        free(old);
        hdr10plus_count--;
    }

    e->inserted_ts = now;
    /* insert at head */
    e->prev = NULL;
    e->next = hdr_lru_head;
    if (hdr_lru_head) hdr_lru_head->prev = e;
    hdr_lru_head = e;
    if (!hdr_lru_tail) hdr_lru_tail = e;
    hdr10plus_count++;
    /* link into hash buckets for fast lookup */
    hdr_link_into_bucket(e);
    pthread_mutex_unlock(&hdr_store_mutex);
    return 0;
}

char *
avpipe_get_hdr10plus(
    int64_t pts)
{
    pthread_mutex_lock(&hdr_store_mutex);
    int64_t now = (int64_t)time(NULL);
    /* First prune expired entries */
    hdr_entry_t *cur = hdr_lru_head;
    while (cur) {
        hdr_entry_t *nxt = cur->next;
        if (hdr10plus_ttl_seconds > 0 && now - cur->inserted_ts > hdr10plus_ttl_seconds) {
            /* unlink cur */
            if (cur->prev) cur->prev->next = cur->next;
            else hdr_lru_head = cur->next;
            if (cur->next) cur->next->prev = cur->prev;
            else hdr_lru_tail = cur->prev;
            free(cur->json);
            hdr_unlink_from_bucket(cur);
            free(cur);
            hdr10plus_count--;
        }
        cur = nxt;
    }

    /* Find nearest PTS within tolerance. Use bucket probing across offsets for speed when tolerance is small. */
    hdr_entry_t *best = NULL;
    int64_t best_diff = LLONG_MAX;
    if (hdr10plus_tolerance_pts == 0) {
        /* exact match: use hash lookup */
        unsigned int idx = hdr_hash_idx(pts);
        hdr_entry_t *hcur = hdr_buckets[idx];
        while (hcur) {
            if (hcur->pts == pts) { best = hcur; best_diff = 0; break; }
            hcur = hcur->hnext;
        }
    } else {
        /* search offsets from 0..tolerance (both sides) */
        int64_t tol = hdr10plus_tolerance_pts;
        for (int64_t off = 0; off <= tol; off++) {
            int64_t candidates[2];
            int nc = 0;
            candidates[nc++] = pts + off;
            if (off != 0) candidates[nc++] = pts - off;
            for (int i = 0; i < nc; i++) {
                unsigned int idx = hdr_hash_idx(candidates[i]);
                hdr_entry_t *hcur = hdr_buckets[idx];
                while (hcur) {
                    if (hcur->pts == candidates[i]) {
                        int64_t diff = llabs(hcur->pts - pts);
                        if (diff < best_diff) { best = hcur; best_diff = diff; }
                        break; /* only one entry per exact pts in hash semantics */
                    }
                    hcur = hcur->hnext;
                }
            }
            if (best_diff == 0) break;
        }
    }

    if (best && (hdr10plus_tolerance_pts == 0 ? best->pts == pts : best_diff <= hdr10plus_tolerance_pts)) {
        /* Return a COPY of the JSON without removing the entry */
        char *json = strdup(best->json);
        pthread_mutex_unlock(&hdr_store_mutex);
        return json;
    }

    pthread_mutex_unlock(&hdr_store_mutex);
    return NULL;
}

/* Configuration API */
int avpipe_hdr10plus_set_tolerance(int64_t tolerance_pts) {
    pthread_mutex_lock(&hdr_store_mutex);
    hdr10plus_tolerance_pts = tolerance_pts;
    pthread_mutex_unlock(&hdr_store_mutex);
    return 0;
}

int avpipe_hdr10plus_set_ttl(int ttl_seconds) {
    pthread_mutex_lock(&hdr_store_mutex);
    hdr10plus_ttl_seconds = ttl_seconds;
    pthread_mutex_unlock(&hdr_store_mutex);
    return 0;
}

int avpipe_hdr10plus_set_capacity(int max_entries) {
    pthread_mutex_lock(&hdr_store_mutex);
    hdr10plus_max_entries = max_entries;
    /* optionally evict least-recently-used (tail) if current count exceeds new max */
    while (hdr10plus_max_entries > 0 && hdr10plus_count > hdr10plus_max_entries) {
        if (!hdr_lru_tail) break;
        hdr_entry_t *old = hdr_lru_tail;
        if (old->prev) old->prev->next = NULL;
        hdr_lru_tail = old->prev;
        if (!hdr_lru_tail) hdr_lru_head = NULL;
        free(old->json);
        hdr_unlink_from_bucket(old);
        free(old);
        hdr10plus_count--;
    }
    pthread_mutex_unlock(&hdr_store_mutex);
    return 0;
}

/* Export API - writes all stored HDR10+ metadata to a JSON file in x265 array format */
int avpipe_export_hdr10plus_to_file(const char *filepath) {
    if (!filepath) return -1;

    pthread_mutex_lock(&hdr_store_mutex);

    /* Collect all entries into an array */
    int count = hdr10plus_count;
    if (count == 0) {
        pthread_mutex_unlock(&hdr_store_mutex);
        /* Write empty array */
        FILE *f = fopen(filepath, "w");
        if (!f) return -1;
        fprintf(f, "[]\n");
        fclose(f);
        return 0;
    }

    hdr_entry_t **entries = (hdr_entry_t **)malloc(count * sizeof(hdr_entry_t *));
    if (!entries) {
        pthread_mutex_unlock(&hdr_store_mutex);
        return -1;
    }

    /* Walk LRU list to collect all entries */
    int idx = 0;
    hdr_entry_t *cur = hdr_lru_head;
    while (cur && idx < count) {
        entries[idx++] = cur;
        cur = cur->next;
    }
    count = idx;

    /* Sort by PTS (bubble sort is fine for small arrays) */
    for (int i = 0; i < count - 1; i++) {
        for (int j = 0; j < count - i - 1; j++) {
            if (entries[j]->pts > entries[j+1]->pts) {
                hdr_entry_t *tmp = entries[j];
                entries[j] = entries[j+1];
                entries[j+1] = tmp;
            }
        }
    }

    /* Write JSON array to file */
    FILE *f = fopen(filepath, "w");
    if (!f) {
        free(entries);
        pthread_mutex_unlock(&hdr_store_mutex);
        return -1;
    }

    fprintf(f, "[\n");
    for (int i = 0; i < count; i++) {
        fprintf(f, "%s", entries[i]->json);
        if (i < count - 1) {
            fprintf(f, ",\n");
        } else {
            fprintf(f, "\n");
        }
    }
    fprintf(f, "]\n");

    fclose(f);
    free(entries);
    pthread_mutex_unlock(&hdr_store_mutex);

    return 0;
}

/* Clear all HDR10+ metadata from the store */
void avpipe_clear_hdr10plus_store(void) {
    pthread_mutex_lock(&hdr_store_mutex);

    /* Free all entries */
    hdr_entry_t *cur = hdr_lru_head;
    while (cur) {
        hdr_entry_t *next = cur->next;
        free(cur->json);
        free(cur);
        cur = next;
    }

    /* Reset all pointers and counters */
    hdr_lru_head = NULL;
    hdr_lru_tail = NULL;
    hdr10plus_count = 0;
    for (int i = 0; i < HDR_HASH_SIZE; i++) {
        hdr_buckets[i] = NULL;
    }

    pthread_mutex_unlock(&hdr_store_mutex);
}

/* Get count of HDR10+ metadata entries in the store */
int avpipe_get_hdr10plus_count(void) {
    pthread_mutex_lock(&hdr_store_mutex);
    int count = hdr10plus_count;
    pthread_mutex_unlock(&hdr_store_mutex);
    return count;
}
