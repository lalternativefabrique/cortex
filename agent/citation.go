package agent

import (
	"context"
	"strconv"
	"sync"
)

type webRefsKey struct{}

type webRefs struct {
	mu    sync.Mutex
	byURL map[string]string
}

func withWebRefs(ctx context.Context) context.Context {
	if _, ok := ctx.Value(webRefsKey{}).(*webRefs); ok {
		return ctx
	}
	return context.WithValue(ctx, webRefsKey{}, &webRefs{byURL: map[string]string{}})
}

// WebRef is the citation reference of a web page within the run ctx belongs
// to: "W1", "W2"… in the order the run meets pages, the same one every time
// it meets the same URL, so a page is one citation however many tools
// returned it. Outside a run, ok is false.
func WebRef(ctx context.Context, url string) (ref string, ok bool) {
	refs, ok := ctx.Value(webRefsKey{}).(*webRefs)
	if !ok {
		return "", false
	}
	refs.mu.Lock()
	defer refs.mu.Unlock()
	if ref, seen := refs.byURL[url]; seen {
		return ref, true
	}
	ref = "W" + strconv.Itoa(len(refs.byURL)+1)
	refs.byURL[url] = ref
	return ref, true
}
