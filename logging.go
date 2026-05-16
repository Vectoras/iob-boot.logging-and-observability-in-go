package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	pkgerr "github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
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

	// handlers and closers

	var (
		handlers []slog.Handler
		closers  []closeFunc
	)

	// stderr - debug

	tintOptions := tint.Options{
		Level:       slog.LevelDebug,
		ReplaceAttr: addStackToErrors,
		NoColor:     true,
	}
	if isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		tintOptions.NoColor = false
	}
	handlers = append(handlers, tint.NewHandler(os.Stderr, &tintOptions))

	// file - info

	logFilePath, ok := os.LookupEnv("LINKO_LOG_FILE")
	if !ok {
		closeF := func() error { return nil }
		return nil, closeF, errors.New("missing 'LINKO_LOG_FILE' environment variable")
	}

	logFile := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    1,
		MaxAge:     28,
		MaxBackups: 10,
		LocalTime:  false,
		Compress:   true,
	}

	handlers = append(handlers, slog.NewJSONHandler(logFile, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: addStackToErrors,
	}))
	closers = append(closers, logFile.Close)

	// close function and the logger

	close := func() error {
		var errs []error
		for _, closer := range closers {
			errs = append(errs, closer())
		}
		return errors.Join(errs...)
	}

	hostname, _ := os.Hostname()
	logger := slog.New(slog.NewMultiHandler(handlers...)).With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostname),
	)

	// ---

	return logger, close, nil

}
