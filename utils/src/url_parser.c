/*
 * A very simple url parser.
 *
 */

#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <netdb.h>
#include <arpa/inet.h>
#include <stdbool.h>

#include "url_parser.h"

void
free_parsed_url(
    url_parser_t *url_parsed)
{
    free(url_parsed->protocol);
}

static char *
find_char(
    char *str,
    char c)
{
    if (!str)
        return NULL;

    char *s = strchr(str, c);
    if (s == NULL)
        return NULL;

    *s = '\0';
    return s+1;
}

/*
 * Parses a url with general format: URL = scheme://host:port/path/?query#fragment
 * (but it doesn't cover fragment).
 *
 * Some examples that it covers:
 *  - udp://127.0.0.1:21001?pkt_size=1316
 *  - udp://127.0.0.1?pkt_size=1316
 *  - udp://127.0.0.1/foo?pkt_size=1316
 *  - http://127.0.0.1:8000/foo?pkt_size=1316
 *  - /foo/bar/filename
 *
 */
int
parse_url(
    char *url,
    url_parser_t *parsed_url)
{
	char *local_url;
	char *colon;
    char *port;
    char *slash;

    if (!url || !parsed_url)
        return 1;

    memset(parsed_url, 0, sizeof(url_parser_t));

	/* Copy url */
    local_url = strdup(url);

    colon = find_char(local_url, ':');
    if (colon == NULL) {
        parsed_url->protocol = strdup("file");
        parsed_url->path = local_url;
        return 0;
    }

    /* Protocol */
    parsed_url->protocol = local_url;

    /* Invalid URL */
    if (colon[0] != '/' && colon[1] != '/')
        return 1;

    /* pass "://" */
    colon = &colon[2];

    port = find_char(colon, ':');
    if (port != NULL) {
        parsed_url->host = colon;
        slash = find_char(port, '/');
        if (slash != NULL)
            parsed_url->query_string = find_char(slash, '?');
        else
            parsed_url->query_string = find_char(port, '?');
        parsed_url->port = port;
        parsed_url->path = slash;
    }

    if (port == NULL) {
        slash = find_char(colon, '/');
        if (slash != NULL)
            parsed_url->query_string = find_char(slash, '?');
        else
            parsed_url->query_string = find_char(colon, '?');
        parsed_url->host = colon;
        parsed_url->path = slash;
    }

	return 0;
}


#if 0
int main(int argc, char **argv) {
	int error;
	url_parser_t parsed_url;

	if (argc == 1) {
		fprintf(stderr, "No URL passed.\n");
		return 1;
	}

	for (int i = 1; i < argc; i++) {
		error = parse_url(argv[i], &parsed_url);
		if (error != 0) {
			fprintf(stderr, "Invalid URL \"%s\".\n", argv[i]);
			continue;
		}

		printf("Protocol: '%s' - Host: '%s' - Port: '%s' - Path: '%s' - Query String: '%s'\n",
			parsed_url.protocol, parsed_url.host, parsed_url.port, parsed_url.path, parsed_url.query_string);
		free_parsed_url(&parsed_url);
	}

}
#endif

