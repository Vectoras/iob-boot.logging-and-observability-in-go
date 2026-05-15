package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
)

type closeFunc func() error

func initializeLogger() (*slog.Logger, closeFunc, error) {
	// generid empty function
	closeF := func() error {
		return nil
	}

	// std logging only
	filePath, ok := os.LookupEnv("LINKO_LOG_FILE")
	if !ok {
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		return logger, closeF, nil
	}

	// file + std logging (production)
	accessFileW, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	accessFileBW := bufio.NewWriterSize(accessFileW, 8192)
	if err != nil {
		return nil, closeF, fmt.Errorf("failed to open %q for writing: %w", filePath, err)
	}
	mw := io.MultiWriter(accessFileBW, os.Stderr)
	logger := slog.New(slog.NewTextHandler(mw, nil))
	// overried the function
	closeF = func() error {
		err = accessFileBW.Flush()
		if err != nil {
			return fmt.Errorf("error when flushing the buffere writter to %q file: %w", filePath, err)
		}

		err := accessFileW.Close()
		if err != nil {
			return fmt.Errorf("error when closing the %q file: %w", filePath, err)
		}

		return nil
	}

	return logger, closeF, nil

}
