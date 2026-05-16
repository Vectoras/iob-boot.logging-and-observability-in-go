package main

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	pkgerr "github.com/pkg/errors"
)

type closeFunc func() error
type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}
type multiError interface {
	error
	Unwrap() []error
}

func errorAttrs(err error) []slog.Attr {
	attrs := []slog.Attr{
		{
			Key:   "message",
			Value: slog.StringValue(err.Error()),
		},
	}
	attrs = append(attrs, linkoerr.Attrs(err)...)
	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		attrs = append(attrs, slog.Attr{
			Key:   "stack_trace",
			Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		})
	}

	return attrs
}

func initializeLogger() (*slog.Logger, closeFunc, error) {

	// log errors with their stack helper function

	addStackToErrors := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == "error" {
			err, ok := a.Value.Any().(error)
			if !ok {
				return a
			}

			if multiErr, ok := errors.AsType[multiError](err); ok {
				var errsGroup []slog.Attr
				for i, err := range multiErr.Unwrap() {
					errsGroup = append(errsGroup, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), errorAttrs(err)...))
				}

				return slog.GroupAttrs("errors", errsGroup...)
			}

			return slog.GroupAttrs(a.Key, errorAttrs(err)...)
		}
		return a
	}

	// stderr - debug

	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: addStackToErrors,
	})

	// file - info

	logFilePath, ok := os.LookupEnv("LINKO_LOG_FILE")
	if !ok {
		logger := slog.New(debugHandler)
		closeF := func() error { return nil }
		return logger, closeF, errors.New("missing 'LINKO_LOG_FILE' environment variable")
	}

	logFileW, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		logger := slog.New(debugHandler)
		closeF := func() error { return nil }
		return logger, closeF, err
	}
	logFileBW := bufio.NewWriterSize(logFileW, 8192)

	infoHandler := slog.NewJSONHandler(logFileBW, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: addStackToErrors,
	})
	logger := slog.New(slog.NewMultiHandler(debugHandler, infoHandler))
	closeF := func() error {
		errFlush := logFileBW.Flush()
		if errFlush != nil {
			errFlush = fmt.Errorf("failed to flush the log file buffer: %w", errFlush)
		}
		errClose := logFileW.Close()
		if errClose != nil {
			errClose = fmt.Errorf("failed to close the log file: %w", errClose)
		}
		return errors.Join(errFlush, errClose)
	}

	hostname, _ := os.Hostname()
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostname),
	)
	
	return logger, closeF, nil

}
