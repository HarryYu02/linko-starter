package main

import (
	"io"
	"log"
	"net/http"
	"os"
)

func initializeLogger() (*log.Logger, error) {
	target := os.Getenv("LINKO_LOG_FILE")
	if len(target) > 0 {
		logFile, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("failed to open %s: %v\n", target, err)
			return nil, err
		}
		multiWriter := io.MultiWriter(os.Stderr, logFile)
		logger := log.New(multiWriter, "", log.LstdFlags)
		return logger, nil
	} else {
		logger := log.New(os.Stderr, "", log.LstdFlags)
		return logger, nil
	}
}

func requestLogger(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Printf("Served request: %s %s", r.Method, r.URL.Path)
		})
	}
}

