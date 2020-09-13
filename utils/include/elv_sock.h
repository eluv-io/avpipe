/*
 * elv_sock.h
 */

#pragma once

int
readable_timeout(
    int fd,
    int sec);

int
udp_socket(
    const char *host,
    const char *port,
    struct sockaddr **saptr,
    socklen_t *lenp);

int
tcp_connect(
    const char *host,
    const char *port);

