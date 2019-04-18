/*
 * elv_log.c
 */

#include <stdio.h>
#include <stdarg.h>
#include <sys/time.h>
#include <time.h>
#include <stdlib.h>
#include <unistd.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <stdbool.h>
#include <string.h>
#include <errno.h>
#include <fcntl.h>

#include "elv_log.h"

#define LOG_BUFF_SIZE       (64*1024)
#define DEFAULT_NB_ROTATE   10

typedef struct elv_logger_t {
    bool _initialized;              // Is it initialized yet?
    char *_dirname;                 // Directory name holding log files
    char *_appname;
    u_int64_t _rotate_size;         // When rotation will happen
    elv_log_level_t _log_level;     // Log level of the logger
    elv_log_appender_t _appender;   // Where the output goes
    u_int32_t _nb_rotate;           // Number of rotation files
    u_int32_t _n_rotate;            // Current rotation number
    int _fd;
    u_int32_t _total_written;       // Total bytes written to the current log file
    elv_logger_f elv_logger[elv_log_error+1];   // Array of loggers for different log levels
} elv_logger_t;


static elv_logger_t _logger;

static const char *get_level_str(int level)
{
    switch (level) {
    case elv_log_debug:
        return "DBG";
    case elv_log_log:
        return "LOG";
    case elv_log_warning:
        return "WARN";
    case elv_log_error:
        return "ERR";
    default:
        return "ERR";
    }
}

static bool
elv_file_exist(
    char *filename)
{
    int err;
    struct stat stb;

    err = stat(filename, &stb);
    if (err != 0)
        return false;
    return true;
}

static int
_find_last_log(
    char *dir,
    char *appname,
    u_int32_t _nb_rotate)
{
    int i;
    char filename[1024];

    for (i=0; i<_nb_rotate; i++) {
        sprintf(filename, "%s/%s-%d.log", dir, appname, i);
        if (!elv_file_exist(filename))
            break;
    }

    if (i > 0)
        return i-1;
    return 0;
}

static void
_panic(
    const char *msg)
{
    fprintf(stderr, "%s\n", msg);
    exit(1);
}

static const char*
_get_level_str(
    elv_log_level_t level)
{
    switch (level) {
    case elv_log_debug:
        return "DBG";
    case elv_log_log:
        return "LOG";
    case elv_log_warning:
        return "WRN";
    case elv_log_error:
        return "ERR";
    }
    _panic("Invalid log level");
    return NULL;
}

int
elv_logger_open(
    char *dir,
    char *appname,
    u_int32_t nb_rotate,
    u_int64_t rotate_size,
    elv_log_appender_t appender)
{
    char buf[1024];

    if (dir && dir[0] != 0)
        _logger._dirname = strdup(dir);
    else
        _logger._dirname = strdup("./");

    if (appname && appname[0] != 0)
        _logger._appname = strdup(appname);
    else
        _logger._appname = strdup("elv");

    _logger._rotate_size = rotate_size;
    _logger._appender = appender;
    if (nb_rotate)
        _logger._nb_rotate = nb_rotate;
    else
        _logger._nb_rotate = DEFAULT_NB_ROTATE;
    _logger._n_rotate = _find_last_log(_logger._dirname, _logger._appname, _logger._nb_rotate);
    _logger._log_level = elv_log_log;

    sprintf(buf, "%s/%s-%d.log", _logger._dirname, _logger._appname, _logger._n_rotate);
    _logger._fd = open(buf, O_WRONLY | O_APPEND | O_CREAT, 0644);
    if (_logger._fd < 0)
        return errno;

    elv_log("Log level is set to %s", _get_level_str(_logger._log_level));
    _logger._initialized = true;
    return 0;
}

int
elv_logger_close()
{
    free(_logger._dirname);
    free(_logger._appname);
    return close(_logger._fd);
}

elv_log_level_t
elv_get_log_level()
{
    return _logger._log_level;
}

void
elv_set_log_level(
    elv_log_level_t level)
{
    _logger._log_level = level;
}

elv_log_appender_t
elv_get_log_appender()
{
    return _logger._appender;
}

void
elv_set_log_appender(
    elv_log_appender_t appender)
{
    _logger._appender = appender;
}

static int
_set_log_header(
    char *buf,
    const char *level_str,
    const char *prefix)
{
    struct timeval tval;
    time_t t;
    struct tm *lt;
    int msec;

    /** obtain mili second */
    gettimeofday(&tval, (struct timezone *) 0);
    msec = tval.tv_usec / 1000;

    /* Get current time */
    t = time(NULL);
    lt = localtime(&t);   

    return sprintf(buf, "%04d-%02d-%02d %02d:%02d:%02d.%03d %s%s ",
        lt->tm_year+1900, lt->tm_mon, lt->tm_mday, lt->tm_hour, lt->tm_min, lt->tm_sec, msec, level_str, prefix);
}

static int
_rotate_log()
{
    char buf[1024];

    if (!(_logger._appender & elv_log_file))
        return 0;

    close(_logger._fd);
    _logger._n_rotate = (_logger._n_rotate+1) % _logger._nb_rotate;
    sprintf(buf, "%s/%s-%d.log", _logger._dirname, _logger._appname, _logger._n_rotate);
    _logger._fd = open(buf, O_WRONLY | O_CREAT | O_TRUNC, 0644);
    if (_logger._fd < 0)
        return errno;

    _logger._total_written = 0;
    return 0;
}

/* TODO: make this thread safe */
static int
_flush_log(
    char *buf,
    int len)
{
    int rc = 0;

    if ( buf[len-1] != '\n' ) {
        buf[len] = '\n';
        buf[len+1] = '\0';
        len++;
    }

    if (_logger._appender & elv_log_stdout || !_logger._initialized)
        fprintf(stdout, "%s", buf);

    if (_logger._appender & elv_log_file)
        rc = write(_logger._fd, buf, len);

    if (rc > 0)
        _logger._total_written += len;

    if (_logger._total_written > _logger._rotate_size)
        _rotate_log();

    return rc;
}

int
elv_vlog(int level, const char *prefix, const char *fmt, va_list vl)
{
    int len = 0;
    char buf[LOG_BUFF_SIZE];

    if (_logger._log_level > level)
        return 0;

    if (_logger.elv_logger[level] == NULL)
        len = _set_log_header(buf, get_level_str(level), prefix);
    
    len += vsnprintf(buf+len, LOG_BUFF_SIZE-len, fmt, vl);

    if (_logger.elv_logger[level] != NULL)
        return _logger.elv_logger[level](buf);

    return _flush_log(buf, len);
}

int
elv_log(
    const char *fmt, ...)
{
    va_list vl;
    va_start(vl, fmt);
    int rc = elv_vlog(elv_log_log, "", fmt, vl);
    va_end(vl);
    return rc;
}

int
elv_dbg(
    const char *fmt, ...)
{
    va_list vl;
    va_start(vl, fmt);
    int rc = elv_vlog(elv_log_debug, "", fmt, vl);
    va_end(vl);
    return rc;
}

int
elv_warn(
    const char *fmt, ...)
{
    va_list vl;
    va_start(vl, fmt);
    int rc = elv_vlog(elv_log_warning, "", fmt, vl);
    va_end(vl);
    return rc;
}

int
elv_err(
    const char *fmt, ...)
{
    va_list vl;
    va_start(vl, fmt);
    int rc = elv_vlog(elv_log_error, "", fmt, vl);
    va_end(vl);
    return rc;
}

int
elv_set_log_func(
    elv_log_level_t level,
    elv_logger_f logger_f)
{
    if (level > elv_log_error)
        return 1;
    _logger.elv_logger[level] = logger_f;
    return 0;
}
