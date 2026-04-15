package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"boot.dev/linko/internal/linkoerr"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	pkgerr "github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
)

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error    error
}

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
	debugHandler := tint.NewHandler(os.Stderr, &tint.Options{
		Level: slog.LevelDebug,
		ReplaceAttr: replaceAttr,
		NoColor: !(isatty.IsCygwinTerminal(os.Stderr.Fd()) || isatty.IsTerminal(os.Stderr.Fd())),
	})

	if len(target) > 0 {
		handlers := make([]slog.Handler, 0)
		handlers = append(handlers, debugHandler)

		fileLogger := &lumberjack.Logger{
			Filename:   target,
			MaxSize:    1,
			MaxAge:     28,
			MaxBackups: 10,
			LocalTime:  false,
			Compress:   true,
		}
		handlers = append(handlers, slog.NewJSONHandler(fileLogger, &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		}))
		logger := slog.New(slog.NewMultiHandler(
			handlers...
		))
		return logger, fileLogger.Close, nil
	} else {
		logger := slog.New(debugHandler)
		return logger, func() error { return nil }, nil
	}
}

func redactIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	// Not valid ip
	if err != nil {
		return address
	}
	ip := net.ParseIP(host)
	// Not valid ip
	if ip == nil {
		return address
	}
	// Not ipv-4
	if ip.To4() == nil {
		return address
	}
	octets := strings.Split(host, ".")
	if len(octets) != 4 {
		return address
	}
	return fmt.Sprintf("%s.%s.%s.x", octets[0], octets[1], octets[2])
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			spyReader := &spyReadCloser{ReadCloser: r.Body}
			r.Body = spyReader

			spyWriter := &spyResponseWriter{ResponseWriter: w}
			logContext := &LogContext{}
			r = r.WithContext(context.WithValue(context.Background(), logContextKey, logContext))
			next.ServeHTTP(spyWriter, r)

			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", redactIP(r.RemoteAddr)),
				slog.Duration("duration", time.Since(start)),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyWriter.statusCode),
				slog.Int("response_body_bytes", spyWriter.bytesWritten),
				slog.String("request_id", r.Header.Get("X-Request-ID")),
			}
			if len(logContext.Username) > 0 {
				attrs = append(attrs, slog.String("user", logContext.Username))
			}
			if logContext.Error != nil {
				attrs = append(attrs, slog.Any("error", logContext.Error))
			}

			logger.Info(
				"Served request",
				attrs...
			)
		})
	}
}

