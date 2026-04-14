package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

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

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (r *spyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func errAttrs(err error) []slog.Attr {
	attrs := make([]slog.Attr, 1)
	attrs[0] = slog.Attr{
		Key:   "message",
		Value: slog.StringValue(err.Error()),
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

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		if multiErr, ok := errors.AsType[multiError](err); ok {
			errs := multiErr.Unwrap()
			attrs := make([]slog.Attr, len(errs))
			for i, e := range errs {
				attrs[i] = slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), errAttrs(e)...)
			}
			return slog.GroupAttrs("errors", attrs...)
		} else {
			return slog.GroupAttrs("error", errAttrs(err)...)
		}
	}
	return a
}

func initializeLogger(target string) (*slog.Logger, closeFunc, error) {
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	})

	if len(target) > 0 {
		logFile, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open %s: %v\n", target, err)
		}
		bufferedFile := bufio.NewWriterSize(logFile, 8192)
		infoHandler := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: replaceAttr,
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
			start := time.Now()

			spyReader := &spyReadCloser{ReadCloser: r.Body}
			r.Body = spyReader

			spyWriter := &spyResponseWriter{ResponseWriter: w}
			next.ServeHTTP(spyWriter, r)

			logger.Info(
				"Served request",
				"method", r.Method,
				"path", r.URL.Path,
				"client_ip", r.RemoteAddr,
				slog.Duration("duration", time.Since(start)),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyWriter.statusCode),
				slog.Int("response_body_bytes", spyWriter.bytesWritten),
			)
		})
	}
}

