#include <stdlib.h>
#include <stdio.h>
#include <pthread.h>
#include <unistd.h>
#include <stdint.h>
#include <inttypes.h>
#include <string.h>
#include "elv_channel.h"

#define CHANNEL_SIZE1       1
#define CHANNEL_SIZE5       5
#define CHANNEL_SIZE100     100
#define CHANNEL_SIZE100000  100000
#define TIMEOUT_SEC         5
#define MSG_COUNT           100000

typedef struct test_thread_params_t {
    elv_channel_t   *channel;
    int             with_sleep;
    int             with_trace;
} test_thread_params_t;

static void *
send_thread_func(
    void *thread_params)
{
    test_thread_params_t *params = (test_thread_params_t *) thread_params;

    for (int i=0; i<MSG_COUNT; i++) {
        int *msg = (int*) malloc(sizeof(int));
        *msg = i;
        if (i%100 == 0 && params->with_sleep)
            usleep(10000);
        elv_channel_send(params->channel, msg);
        if (params->with_trace)
            printf("Sent %d, size=%"PRId64"\n", *msg, elv_channel_size(params->channel));
    }

    return NULL;
}


static void *
recv_thread_func(
    void *thread_params)
{
    test_thread_params_t *params = (test_thread_params_t *) thread_params;
    int *msg;

    for (int i=0; i<MSG_COUNT; i++) {
        elv_channel_timed_receive(params->channel, TIMEOUT_SEC*1000000, (void **)&msg);
        if (params->with_trace)
            printf("Received %d, size=%"PRId64"\n", *msg, elv_channel_size(params->channel));
        free(msg);
    }

    return NULL;
}

static void
test_send_recv(
    int channel_size,
    int do_sleep,
    int do_trace)
{
    pthread_t               recv_tid;
    pthread_t               send_tid;
    test_thread_params_t    params;

    memset(&params, 0, sizeof(test_thread_params_t));
    params.with_sleep = do_sleep;
    params.with_trace = do_trace;
    elv_channel_init(&params.channel, channel_size, NULL);
    pthread_create(&recv_tid, NULL, recv_thread_func, &params);
    pthread_create(&send_tid, NULL, send_thread_func, &params);
    pthread_join(send_tid, NULL);
    pthread_join(recv_tid, NULL);
    if (elv_channel_size(params.channel) != 0)
        printf("Test CHANNEL_SIZE%d failed line %d\n", channel_size, __LINE__);
    elv_channel_fini(&params.channel);
}

int
main() {
    int do_trace = 0;

    test_send_recv(CHANNEL_SIZE1, 0, do_trace);
    test_send_recv(CHANNEL_SIZE5, 0, do_trace);
    test_send_recv(CHANNEL_SIZE100, 0, do_trace);
    test_send_recv(CHANNEL_SIZE100000, 0, do_trace);

    test_send_recv(CHANNEL_SIZE1, 1, do_trace);
    return 0;
}
