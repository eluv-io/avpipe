/*
 * elv_log.h
 */

#pragma once

#include <stdio.h>

typedef enum elv_log_level_t {
    elv_log_debug = 0,
    elv_log_log,
    elv_log_warning,
    elv_log_error
} elv_log_level_t;

typedef enum elv_log_appender_t {
    elv_log_file = 1,
    elv_log_stdout = 2
} elv_log_appender_t;

typedef int
(*elv_logger_f)(
    char *msg);

/**
 * @brief   Initializes the Eluvio logger. If the application doesn't initialize the logger,
 *          any subsequent calls to elv_dbg()/elv_log()/elv_warn()/elv_err() will append the
 *          log message to the stdout.
 *
 * @param   dir             If dir exist then the log files will be set up in dir directory.
 *                          If dir is NULL, then the current directory holds the log files.
 * @param   nb_rotate       Number of rotating log files.
 * @param   rotate_size     Each log file will be rotate if the size of log file >= rotate_size.
 * @param   appender        Determines the output of the log messages.
 *                          elv_log_file: means the log messages will be appended to the log file.
 *                          elv_log_stdout: means the log messages will be appended to stdout.
 *
 * @return  0               means success
 *                          otherwise error code
 */
int
elv_logger_open(
    char *dir,
    char *appname,
    u_int32_t nb_rotate,
    u_int64_t rotate_size,
    elv_log_appender_t appender);

/*
 * @brief   Closes the log files and releases the resources of logger.
 *
 * @return  0 on success, otherwise error number.
 */
int
elv_logger_close();

/*
 * @brief   Returns the log level.
 *
 * @return  Returns the log level.
 */    
elv_log_level_t
elv_get_log_level();

/*
 * @brief   Sets the log level.
 *
 * @return  void
 */
void
elv_set_log_level(
    elv_log_level_t level);

/*
 * @brief   Gets the log appender.
 *
 * @return  the log appender.
 */
elv_log_appender_t
elv_get_log_appender();

/*
 * @brief   Sets the log appender.
 *
 * @return  void
 */
void
elv_set_log_appender(
    elv_log_appender_t appender);

int
elv_vlog(
    int level, const char *prefix, const char *fmt, va_list vl);

/*
 * @brief   Logs a message with log level elv_log_log.
 *
 * @param   fmt ...     Format message with parameters
 *
 * @return  Number of bytes written to the log file if it is successful.
 *          Error number if it fails.
 */
int
elv_log(
    const char *fmt, ...);

/*
 * @brief   Logs a message with log level elv_log_debug.
 *
 * @param   fmt ...     Format message with parameters
 *
 * @return  Number of bytes written to the log file if it is successful.
 *          Error number if it fails.
 */
int
elv_dbg(
    const char *fmt, ...);

/*
 * @brief   Logs a message with log level elv_log_warning.
 *
 * @param   fmt ...     Format message with parameters
 *
 * @return  Number of bytes written to the log file if it is successful.
 *          Error number if it fails.
 */
int
elv_warn(
    const char *fmt, ...);

/*
 * @brief   Logs a message with log level elv_log_error.
 *
 * @param   fmt ...     Format message with parameters
 *
 * @return  Number of bytes written to the log file if it is successful.
 *          Error number if it fails.
 */
int
elv_err(
    const char *fmt, ...);


/*
 * @brief   Sets the callback log function for the specified log level
 *
 * @param   level       Log level
 * @param   logger_f    Log function to be called for specified log level
 *
 * @return  0 if successful, otherwise 1.
 */
int
elv_set_log_func(
    elv_log_level_t level,
    elv_logger_f logger_f);
