package internal

import (
	"context"
	"net/http"
	"time"
)

const (
	httpReadHeaderTimeout  = 10 * time.Second
	httpReadTimeout        = 15 * time.Second
	httpWriteTimeout       = 30 * time.Second
	httpIdleTimeout        = 120 * time.Second
	upstreamRequestTimeout = 30 * time.Second
)

func moduleHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    65536,
	}
}

func handlerContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), upstreamRequestTimeout)
}
