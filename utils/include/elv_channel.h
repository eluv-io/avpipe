/*
 * elv_channel.h
 */

#pragma once

typedef struct elv_channel_t elv_channel_t;

int
elv_channel_init(
    elv_channel_t **channel,
    u_int64_t capacity);

int
elv_channel_send(
    elv_channel_t *channel,
    void *msg);

int
elv_channel_timed_receive(
    elv_channel_t *channel,
    u_int64_t usec, 
    void **rcvdmsg);

void *
elv_channel_receive(
    elv_channel_t *channel);

int64_t
elv_channel_size(
    elv_channel_t *channel);

int
elv_channel_close(
    elv_channel_t *channel);

int
elv_channel_fini(
    elv_channel_t **channel);
