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

/*
 * Join the IPv4 multicast group 'group_addr'.
 * No-op if the address is not multicast.
 * Optionally specify 'local_addr' as the receiving interface.
 * (NULL or an empty string selects the system default interface)
 */
int
udp_join_multicast(
    int sockfd,
    const struct sockaddr *group_addr,
    socklen_t group_addr_len,
    const char *local_addr);

int
tcp_connect(
    const char *host,
    const char *port);

int
set_sock_nonblocking(
    int sock);
