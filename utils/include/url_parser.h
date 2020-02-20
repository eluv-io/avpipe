/*
 * url_parser.h
 */

#pragma once

#define UNKNOWN_PORT     (-1)

typedef struct url_parser_t {
    char    *protocol;
    char    *host;
    char    *port;
    char    *path;
    char    *query_string;
} url_parser_t;

int
parse_url(
    char *url,
    url_parser_t *parsed_url);

void
free_parsed_url(
    url_parser_t *url_parsed);


