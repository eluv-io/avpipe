
int
elv_io_open(
    struct AVFormatContext *s,
    AVIOContext **pb,
    const char *url,
    int flags,
    AVDictionary **options);

void
elv_io_close(
    struct AVFormatContext *s,
    AVIOContext *pb);
