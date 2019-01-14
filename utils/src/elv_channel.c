/*
 * elv_channel.c
 */

#include <pthread.h>
#include <stdlib.h>

#include "elv_channel.h"

struct elv_channel_t
{
    void **_items;
    int64_t _count;
    int64_t _front;
    int64_t _rear;
    u_int64_t _capacity;
    pthread_mutex_t _mutex;
    pthread_cond_t _cond_send;  /* Signaled when an item has been sent */
    pthread_cond_t _cond_recv;  /* Signaled when an item has been received */
};

int
elv_channel_init(
    elv_channel_t **channel,
    u_int64_t capacity)
{
    elv_channel_t *ch;

    if (!channel || !capacity)
        return -1;

    ch = (elv_channel_t *) calloc(1, sizeof(elv_channel_t));
    ch->_items = (void **) calloc(1, capacity*sizeof(void*));
    ch->_rear = -1;
    ch->_capacity = capacity;
    pthread_mutex_init(&ch->_mutex, NULL);
    pthread_cond_init(&ch->_cond_send, NULL);
    pthread_cond_init(&ch->_cond_recv, NULL);
    *channel = ch;

    return 0;
}

int
elv_channel_send(
    elv_channel_t *channel,
    void *msg)
{
    if (!channel)
        return -1;

    pthread_mutex_lock(&channel->_mutex);
    while (channel->_count >= channel->_capacity)
        pthread_cond_wait(&channel->_cond_recv, &channel->_mutex);

    channel->_count++;
    channel->_rear = (channel->_rear+1) % channel->_capacity;
    channel->_items[channel->_rear] = msg;
    pthread_cond_signal(&channel->_cond_send);
    pthread_mutex_unlock(&channel->_mutex);

    return 0;
}

void *
elv_channel_receive(
    elv_channel_t *channel)
{
    void *msg;
    if (!channel)
        return NULL;

    pthread_mutex_lock(&channel->_mutex);
    if (channel->_count <= 0)
        pthread_cond_wait(&channel->_cond_send, &channel->_mutex);

    channel->_count--;
    msg = channel->_items[channel->_front];
    channel->_front = (channel->_front+1) % channel->_capacity;
    pthread_cond_signal(&channel->_cond_recv);
    pthread_mutex_unlock(&channel->_mutex);

    return msg;
}

int64_t
elv_channel_size(
    elv_channel_t *channel)
{
    int64_t count;

    if (!channel)
        return -1;

    pthread_mutex_lock(&channel->_mutex);
    count = channel->_count;
    pthread_mutex_unlock(&channel->_mutex);

    return count;
}

int
elv_channel_close(
    elv_channel_t *channel)
{
    return 0;
}

int
elv_channel_fini(
    elv_channel_t **channel)
{
    elv_channel_t *ch;

    if (!channel || !*channel)
        return -1;

    ch = *channel;

    pthread_mutex_destroy(&ch->_mutex);
    pthread_cond_destroy(&ch->_cond_send);
    pthread_cond_destroy(&ch->_cond_recv);
    free(ch->_items);
    free(ch);
    return 0;
}

