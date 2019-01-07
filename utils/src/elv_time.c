/*
 * elv_time.c
 */

#include "elv_time.h"

int
elv_get_time(
    struct timeval *tv)
{
    return gettimeofday(tv, NULL);
}

int
elv_since(
    struct timeval *tv,
    u_int64_t *since)
{
    struct timeval tv2;

    if (gettimeofday(&tv2, NULL) < 0)
        return -1;

    /* If tv2 < tv return -1 */
    if (tv2.tv_sec < tv->tv_sec || (tv2.tv_sec == tv->tv_sec && tv2.tv_usec < tv->tv_usec))
        return -1;
    *since = (tv2.tv_sec-tv->tv_sec)*1000000+tv2.tv_usec-tv->tv_usec;
    return 0;
}
