package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"boot.dev/linko/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (r *spyReadCloser) Read(p []byte) (n int, err error) {
	n, err = r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error    error
}

func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if logCtx, ok := ctx.Value(logContextKey).(*LogContext); ok {
		logCtx.Error = err
	}
	if status == 401 || status == 403 || status == 500 {
		http.Error(w, http.StatusText(status), status)
	} else {
		http.Error(w, err.Error(), status)
	}
}

func ensureRequestId(next http.Handler) http.Handler {
	return (http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestId := r.Header.Get("X-Request-ID")
		if requestId == "" {
			requestId = rand.Text()
		}
		w.Header().Set("X-Request-ID", requestId)

		next.ServeHTTP(w, r)
	}))
}

func redactIP(address string) string {

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}

	parsedHost := net.ParseIP(host)
	if parsedHost == nil || parsedHost.To4() == nil {
		return address
	}

	parts := strings.Split(host, ".")
	parts[3] = "x"
	redacted := strings.Join(parts, ".")

	// if port != "" {
	// 	redacted = net.JoinHostPort(redacted, port)
	// }

	return redacted
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			r = r.WithContext(context.WithValue(r.Context(), logContextKey, &LogContext{}))

			spyReader := &spyReadCloser{ReadCloser: r.Body}
			r.Body = spyReader
			spyResponseWriter := &spyResponseWriter{ResponseWriter: w}

			next.ServeHTTP(spyResponseWriter, r)

			logAttrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.EscapedPath()),
				slog.String("client_ip", redactIP(r.RemoteAddr)),
				slog.Duration("duration", time.Since(start)),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyResponseWriter.statusCode),
				slog.Int("response_body_bytes", spyResponseWriter.bytesWritten),
				slog.String("request_id", spyResponseWriter.Header().Get("X-Request-ID")),
			}

			if logContext, ok := r.Context().Value(logContextKey).(*LogContext); ok {
				if logContext.Username != "" {
					logAttrs = append(logAttrs, slog.String("user", logContext.Username))
				}
				if logContext.Error != nil {
					logAttrs = append(logAttrs, slog.Any("error", logContext.Error))
				}
			}

			logger.Info("Served request", logAttrs...)
		})
	}
}

func newServer(store store.Store, port int, logger *slog.Logger, cancel context.CancelFunc) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: ensureRequestId(requestLogger(logger)(mux)),
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger:     logger,
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /", s.handlerIndex)
	apiMux.Handle("POST /api/login", s.authMiddleware(http.HandlerFunc(s.handlerLogin)))
	apiMux.Handle("POST /api/shorten", s.authMiddleware(http.HandlerFunc(s.handlerShortenLink)))
	apiMux.Handle("GET /api/stats", s.authMiddleware(http.HandlerFunc(s.handlerStats)))
	apiMux.Handle("GET /api/urls", s.authMiddleware(http.HandlerFunc(s.handlerListURLs)))
	apiMux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	apiMux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)

	mux.Handle("/", otelhttp.NewHandler(metricsMiddleware(apiMux), "http.server"))
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.Handle("GET /debug/pprof/", s.authMiddleware(http.HandlerFunc(pprof.Index)))
	mux.Handle("GET /debug/pprof/profile", s.authMiddleware(http.HandlerFunc(pprof.Profile)))

	return s
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	s.logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d", ln.Addr().(*net.TCPAddr).Port))
	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}
