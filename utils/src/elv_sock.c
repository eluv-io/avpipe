#include <stdlib.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <poll.h>
#include <errno.h>


#include "elv_sock.h"
#include "elv_log.h"

/*
 * Use poll() rather than select(): fd_set only holds FD_SETSIZE (1024)
 *
 * Returns > 0 if the descriptor is readable, 0 on timeout, -1 on error (errno set).
 */
int
readable_timeout(
    int fd,
    int sec)
{
    struct pollfd	pfd;

    pfd.fd = fd;
    pfd.events = POLLIN;
    pfd.revents = 0;

    /* > 0 if descriptor is readable */
    return(poll(&pfd, 1, sec*1000));
}

int
udp_socket(
    const char *host,
    const char *port,
    struct sockaddr **saptr,
    socklen_t *lenp)
{
    int             sockfd, n;
    struct addrinfo hints, *res, *ressave;

    if (!host || !port)
        return -1;

    bzero(&hints, sizeof(struct addrinfo));
    hints.ai_family = AF_INET;
    hints.ai_socktype = SOCK_DGRAM;

    if ( (n = getaddrinfo(host, port, &hints, &res)) != 0)
        return -1;
    ressave = res;

    do {
        sockfd = socket(res->ai_family, res->ai_socktype, res->ai_protocol);
        if (sockfd >= 0)
            break;      /* success */
    } while ( (res = res->ai_next) != NULL);

    if (res == NULL) {   /* errno set from final socket() */
        return -1;
    }

    *saptr = malloc(res->ai_addrlen);
    memcpy(*saptr, res->ai_addr, res->ai_addrlen);
    *lenp = res->ai_addrlen;

    freeaddrinfo(ressave);

    return(sockfd);
}

int
udp_join_multicast(
    int sockfd,
    const struct sockaddr *group_addr,
    socklen_t group_addr_len,
    const char *local_addr)
{
    const struct sockaddr_in *group;
    struct ip_mreq membership;

    if (!group_addr || group_addr->sa_family != AF_INET ||
        group_addr_len < sizeof(struct sockaddr_in)) {
        return 0;
    }

    group = (const struct sockaddr_in *)group_addr;
    if (!IN_MULTICAST(ntohl(group->sin_addr.s_addr))) {
        return 0;
    }

#ifdef IP_MULTICAST_ALL
    /*
     * Linux otherwise delivers traffic for every group joined on this port
     * to every socket bound to the port. Keep each live input scoped to the
     * group it explicitly joined.
     */
    const int multicast_all = 0;
    if (setsockopt(sockfd, IPPROTO_IP, IP_MULTICAST_ALL,
            &multicast_all, sizeof(multicast_all)) < 0) {
        return -1;
    }
#endif

    memset(&membership, 0, sizeof(membership));
    membership.imr_multiaddr = group->sin_addr;
    membership.imr_interface.s_addr = htonl(INADDR_ANY);
    if (local_addr && local_addr[0] != '\0' &&
        inet_pton(AF_INET, local_addr, &membership.imr_interface) != 1) {
        errno = EINVAL;
        return -1;
    }

    return setsockopt(sockfd, IPPROTO_IP, IP_ADD_MEMBERSHIP,
        &membership, sizeof(membership));
}

int
tcp_connect(
    const char *hostname,
    const char *port)
{
    struct hostent *he;
    int sockfd;
    struct sockaddr_in server_addr;
    int port_num = atoi(port);

    sockfd = socket(AF_INET, SOCK_STREAM, 0);
    if (sockfd == -1)
        return -1;

    // Resolve the hostname to an IP address
    if ((he = gethostbyname(hostname)) == NULL)
        return -1;

    // Set up the server address structure
    memset(&server_addr, 0, sizeof(server_addr));
    server_addr.sin_family = AF_INET;
    server_addr.sin_port = htons(port_num);
    server_addr.sin_addr = *((struct in_addr *)he->h_addr);

    if (connect(sockfd, (struct sockaddr *)&server_addr, sizeof(server_addr)) == -1)
        return -1;

    return sockfd;
}

int
set_sock_nonblocking(
    int sock)
{
    int flags = fcntl(sock, F_GETFL);
    if (flags == -1)
        return -1;

    return fcntl(sock, F_SETFL, flags | O_NONBLOCK);
}

#if 0
int
main()
{
    ssize_t             n;
    const int           on = 1;
    socklen_t           salen, len;
    struct sockaddr     *sa, ca;
    char                buf[64*1024];
    int                 sockfd;

    sockfd = udp_socket("127.0.0.1", "21001", &sa, &salen);
    if (sockfd < 0) {
        elv_err("Failed to initialize udp socket");
        return -1;
    }

    setsockopt(sockfd, SOL_SOCKET, SO_REUSEADDR, &on, sizeof(on));
    if (bind(sockfd, sa, salen) < 0) {
        /* Can not bind, fail and exit */
        return -1;
    }

    int pkt_num = 0;
    for ( ; ; ) {
        len = salen;
        n = recvfrom(sockfd, buf, sizeof(buf), 0, &ca, &len);
        pkt_num++;
        elv_log("Received UDP packet=%d, len=%d", pkt_num, n);
    }

    return 0;
}
#endif
