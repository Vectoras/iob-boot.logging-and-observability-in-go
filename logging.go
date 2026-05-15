package main

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

type closeFunc func() error

func initializeLogger() (*slog.Logger, closeFunc, error) {

	// stderr - debug

	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})

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

	infoHandler := slog.NewTextHandler(logFileBW, &slog.HandlerOptions{Level: slog.LevelInfo})	
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
	return logger, closeF, nil
	
}
