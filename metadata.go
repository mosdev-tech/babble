package babble

import (
	"context"
	"net/http"
)

// Хендлер видит только DTO, поэтому всё, что приезжает в заголовках —
// авторизация, request-id, трассировка — кладётся в context один раз на входе и
// достаётся через Metadata.

type metaKey struct{}

type respHeaderKey struct{}

// Meta — заголовки входящего запроса, доступные только на чтение.
type Meta struct {
	h http.Header
}

func (m Meta) Get(key string) string {
	if m.h == nil {
		return ""
	}
	return m.h.Get(key)
}

func (m Meta) Values(key string) []string {
	if m.h == nil {
		return nil
	}
	return m.h.Values(key)
}

// Metadata возвращает заголовки входящего запроса. Вне обработки запроса
// возвращает пустой Meta, а не nil — вызывающему не нужно проверять.
func Metadata(ctx context.Context) Meta {
	m, _ := ctx.Value(metaKey{}).(Meta)
	return m
}

// SetResponseHeader ставит заголовок ответа из хендлера. Вне обработки запроса
// — no-op.
func SetResponseHeader(ctx context.Context, key, value string) {
	h, _ := ctx.Value(respHeaderKey{}).(http.Header)
	if h != nil {
		h.Set(key, value)
	}
}

func withMetadata(ctx context.Context, in http.Header, out http.Header) context.Context {
	ctx = context.WithValue(ctx, metaKey{}, Meta{h: in})
	return context.WithValue(ctx, respHeaderKey{}, out)
}
