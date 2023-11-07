/*
 * avpipe_udp_thread.c
 *
 * Thread function to read UDP/MPEGTS datagrams and push them into udp channel.
 *
 */
#include <sys/types.h>
#include <sys/socket.h>

#include "avpipe_xc.h"
#include "elv_channel.h"
#include "elv_sock.h"
#include "elv_log.h"

typedef struct udp_thread_params_t {
    int             fd;             /* Socket fd to read UDP datagrams */
    elv_channel_t   *udp_channel;   /* udp channel to keep incomming UDP packets */
    socklen_t       salen;
    ioctx_t         *inctx;
} udp_thread_params_t;

/*
 * Returns 0 if the reading UDP datagaram is successful, otherwise corresponding errno will be returned.
 */
int
read_one_udp_datagram(
    int sock,
    udp_packet_t *udp_packet,
    socklen_t *len)
{
    struct sockaddr_storage sa;

try_again:
    udp_packet->len = recvfrom(sock, udp_packet->buf, MAX_UDP_PKT_LEN, 0, (struct sockaddr *) &sa, len);
    if (udp_packet->len < 0) {
        if (errno == EINTR) {
            elv_dbg("Got EINTR recvfrom");
            goto try_again;
        }
        return errno;
    }

    /** Successful read */
    return 0;
}

void *
udp_thread_func(
    void *thread_params)
{
    udp_thread_params_t *params = (udp_thread_params_t *) thread_params;
    xcparams_t *xcparams = params->inctx->params;
    int debug_frame_level = (xcparams != NULL) ? xcparams->debug_frame_level : 0;
    char *url = (xcparams != NULL) ? xcparams->url : "";
    socklen_t len;
    udp_packet_t *udp_packet;
    int ret;
    int first = 1;
    int pkt_num = 0;
    int timedout = 0;
    int connection_timeout = xcparams->connection_timeout;

    for ( ; ; ) {
        if (params->inctx->closed)
            break;

        ret = readable_timeout(params->fd, 1);
        if (ret == -1) {
            if (errno == EINTR) {
                elv_dbg("Got EINTR select");
                continue;
            }
            elv_err("UDP select error fd=%d, err=%d, url=%s", params->fd, errno, url);
            break;
        } else if (ret == 0) {
            /* If no packet has not received yet, check connection_timeout */
            if (first) {
                if (connection_timeout > 0) {
                    connection_timeout--;
                    if (connection_timeout == 0) {
                        elv_channel_close(params->udp_channel, 1);
                        break;
                    }
                }
                continue;
            }

            elv_log("UDP recv fd=%d, url=%s, errno=%d, timeout=%dsec, pkt_num=%d", params->fd, url, errno, timedout+1, pkt_num);
            if (timedout++ == UDP_PIPE_TIMEOUT) {
                elv_err("UDP recv timeout fd=%d, url=%s, errno=%d, pkt_num=%d", params->fd, url, errno, pkt_num);
                break;
            }
            continue;
        }

recv_again:
        len = params->salen;
        udp_packet = (udp_packet_t *) calloc(1, sizeof(udp_packet_t));

        if (params->inctx->closed)
            break;
        ret = read_one_udp_datagram(params->fd, udp_packet, &len);
        if (ret == EAGAIN || ret == EWOULDBLOCK) {
            free(udp_packet);
            timedout = 0;
            continue;
        }

        if (ret != 0) {
            elv_err("UDP recvfrom fd=%d, errno=%d, url=%s", params->fd, errno, url);
            break;
        }

        if (udp_packet->len == 0) {
            free(udp_packet);
            goto recv_again;
        }

        if (first) {
            first = 0;
            elv_log("UDP FIRST url=%s", url);
        }

        pkt_num++;
        udp_packet->pkt_num = pkt_num;
        /* If the channel is closed, exit the thread */
        if (elv_channel_send(params->udp_channel, udp_packet) < 0) {
            break;
        }
        //elv_dbg("Rcv UDP packet=%d, len=%d", pkt_num, udp_packet->len);
        if (debug_frame_level)
            elv_dbg("Received UDP packet=%d, len=%d, url=%s", pkt_num, udp_packet->len, url);
        timedout = 0;
        goto recv_again;
    }

    elv_log("UDP thread terminated, url=%s", url);
    return NULL;
}

