package internal

import "context"

type ctxKey int

const (
	ctxRequestPoster ctxKey = iota
)

func withRequestPoster(ctx context.Context, poster string) context.Context {
	if poster == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxRequestPoster, poster)
}

func requestPosterFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxRequestPoster).(string)
	return v
}
