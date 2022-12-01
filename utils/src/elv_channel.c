/*
 * elv_channel.c
 */

#include <pthread.h>
#include <stdlib.h>
#include <errno.h>
#include <sys/time.h>

#include "elv_channel.h"
#include "elv_log.h"

struct elv_channel_t
{
    void **         _items;
    int64_t         _count;
    int64_t         _front;
    int64_t         _rear;
    u_int64_t       _capacity;
    volatile int    _closed;
    free_elem_f     _free_elem;
    pthread_mutex_t _mutex;
    pthread_cond_t  _cond_send;  /* Signaled when an item has been sent */
    pthread_cond_t  _cond_recv;  /* Signaled when an item has been received */
};

int
elv_channel_init(
    elv_channel_t **channel,
    u_int64_t capacity,
    free_elem_f free_elem)
{
    elv_channel_t *ch;

    if (!channel || !capacity)
        return -1;

    ch = (elv_channel_t *) calloc(1, sizeof(elv_channel_t));
    ch->_items = (void **) calloc(1, capacity*sizeof(void*));
    ch->_rear = -1;
    ch->_capacity = capacity;
    ch->_free_elem = free_elem;
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
    if (!channel || channel->_closed)
        return -1;

    pthread_mutex_lock(&channel->_mutex);
    while (channel->_count >= channel->_capacity) {
        pthread_cond_wait(&channel->_cond_recv, &channel->_mutex);
    }

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
    while (channel->_count <= 0 && !channel->_closed)
        pthread_cond_wait(&channel->_cond_send, &channel->_mutex);

    if (channel->_closed && channel->_count == 0) {
        pthread_cond_signal(&channel->_cond_recv);
        pthread_mutex_unlock(&channel->_mutex);
        return NULL;
    }

    channel->_count--;
    msg = channel->_items[channel->_front];
    channel->_front = (channel->_front+1) % channel->_capacity;
    pthread_cond_signal(&channel->_cond_recv);
    pthread_mutex_unlock(&channel->_mutex);

    return msg;
}

int
elv_channel_timed_receive(
    elv_channel_t *channel,
    u_int64_t usec,
    void **rcvdmsg)
{
    struct timeval tv;
    struct timespec ts;
    void *msg;
    int rc;

    if (!channel)
        return EINVAL;

    *rcvdmsg = NULL;
    gettimeofday(&tv, NULL);

    tv.tv_sec += usec / MICRO_IN_SEC;
    tv.tv_usec += usec % MICRO_IN_SEC;
    if (tv.tv_usec >= MICRO_IN_SEC) {
        tv.tv_sec += (tv.tv_usec / MICRO_IN_SEC);
        tv.tv_usec %= MICRO_IN_SEC;
    }

    ts.tv_sec  = tv.tv_sec;
    ts.tv_nsec = tv.tv_usec * 1000;

    pthread_mutex_lock(&channel->_mutex);
    while (channel->_count <= 0 && !channel->_closed) {
        rc = pthread_cond_timedwait(&channel->_cond_send, &channel->_mutex, &ts);
        /* ETIMEDOUT is not a real error */
        if (rc != 0) {
            elv_log("XXX pthread_cond_timedwait rc=%d, count=%d, front=%d, rear=%d", rc, channel->_count, channel->_front, channel->_rear);
            pthread_mutex_unlock(&channel->_mutex);
            return rc;
        }
    }

    if (channel->_closed) {
        pthread_mutex_unlock(&channel->_mutex);
        return EPIPE;
    }

    channel->_count--;
    msg = channel->_items[channel->_front];
    channel->_front = (channel->_front+1) % channel->_capacity;
    *rcvdmsg = msg;
    pthread_cond_signal(&channel->_cond_recv);
    pthread_mutex_unlock(&channel->_mutex);

    return 0;
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
    elv_channel_t *channel,
    int purge_channel)
{
    channel->_closed = 1;
    pthread_mutex_lock(&channel->_mutex);
    pthread_cond_signal(&channel->_cond_recv);
    pthread_cond_signal(&channel->_cond_send);

    elv_log("XXX elv_channel_close count=%d, front=%d, rear=%d", channel->_count, channel->_front, channel->_rear);
    /* Purge the channel if the flag is set */
    while (purge_channel && channel->_count > 0) {
        channel->_count--;
        void *msg = channel->_items[channel->_front];
        channel->_front = (channel->_front+1) % channel->_capacity;
        if (channel->_free_elem)
            channel->_free_elem(msg);
        else
            free(msg);
    }

    pthread_mutex_unlock(&channel->_mutex);
    
    return 0;
}

int
elv_channel_is_closed(
    elv_channel_t *channel)
{
    return channel->_closed;
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

