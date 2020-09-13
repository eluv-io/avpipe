#include <stdlib.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <sys/select.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <stdlib.h>
#include <string.h>

#include "elv_sock.h"
#include "elv_log.h"

int
readable_timeout(
    int fd,
    int sec)
{
    fd_set			rset;
    struct timeval	tv;

    FD_ZERO(&rset);
    FD_SET(fd, &rset);

    tv.tv_sec = sec;
    tv.tv_usec = 0;

    /* > 0 if descriptor is readable */
    return(select(fd+1, &rset, NULL, NULL, &tv));
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
tcp_connect(
    const char *host,
    const char *port)
{
    int sockfd, n;
    struct addrinfo hints, *res;

    sockfd = socket(AF_INET, SOCK_STREAM, 0);
    if (sockfd == -1)
        return -1;

    if ( (n = getaddrinfo(host, port, &hints, &res)) != 0)
        return -1;

    if (connect(sockfd, res->ai_addr, res->ai_addrlen) != 0)
        return -1;

    return sockfd;
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
