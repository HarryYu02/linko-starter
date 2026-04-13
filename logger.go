package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

type closeFunc func() error

func initializeLogger(target string) (*slog.Logger, closeFunc, error) {
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	if len(target) > 0 {
		logFile, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open %s: %v\n", target, err)
		}
		bufferedFile := bufio.NewWriterSize(logFile, 8192)
		infoHandler := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		logger := slog.New(slog.NewMultiHandler(
			debugHandler,
			infoHandler,
		))
		return logger, bufferedFile.Flush , nil
	} else {
		logger := slog.New(debugHandler)
		return logger, func() error { return nil }, nil
	}
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Info(
				"Served request",
				"method", r.Method,
				"path", r.URL.Path,
				"client_ip", r.RemoteAddr,
			)
		})
	}
}

