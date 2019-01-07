/*
 * elv_time.h
 */

#pragma once

#include <sys/types.h>
#include <sys/time.h>
#include <time.h>

int
elv_get_time(
    struct timeval *tv);

int
elv_since(
    struct timeval *tv,
    u_int64_t *since);
