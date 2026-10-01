package host

import (
	"context"
	"net/http"
)

type callerGoneKey struct{}

// watchCaller keeps the request's end reachable from the turn: a2a-go runs a
// task under a context detached from the request, but keeps its values.
func watchCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), callerGoneKey{}, r.Context().Done())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// untilCallerLeaves ends ctx when the caller that sent the turn went away:
// nobody reads the rest of the answer, so it is not paid for.
func untilCallerLeaves(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	gone, _ := ctx.Value(callerGoneKey{}).(<-chan struct{})
	if gone == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-gone:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
