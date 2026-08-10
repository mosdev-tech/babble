package babble

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type in struct {
	V int `json:"v"`
}

type out struct {
	V int `json:"v"`
}

func testClient(t *testing.T, h http.HandlerFunc, opts ...ClientOption) Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	fast := RetryPolicy{Attempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
	all := append([]ClientOption{WithBaseURL(ts.URL), WithRetry(fast)}, opts...)
	c, err := NewClient(all...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func TestRetryOnlyForIdempotent(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	var o out
	err := c.Invoke(context.Background(), "m", &in{}, &o, CallOpts{Idempotent: false})
	if err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("non-idempotent method retried: %d attempts", got)
	}

	calls.Store(0)
	err = c.Invoke(context.Background(), "m", &in{}, &o, CallOpts{Idempotent: true})
	if err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("idempotent method: want 3 attempts, got %d", got)
	}
}

// 500 означает «сервер начал выполнять и упал» — повтор может задвоить эффект.
func TestNoRetryOn500(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusInternalServerError, jsendError{Status: "error", Message: "boom"})
	})

	var o out
	err := c.Invoke(context.Background(), "m", &in{}, &o, CallOpts{Idempotent: true})
	var se *ServerError
	if !errors.As(err, &se) {
		t.Fatalf("want ServerError, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("500 was retried: %d attempts", got)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		check  func(error) bool
	}{
		{"400", http.StatusBadRequest, func(err error) bool { _, ok := AsValidationError(err); return ok }},
		{"500", http.StatusInternalServerError, func(err error) bool { _, ok := AsServerError(err); return ok }},
		{"418", http.StatusTeapot, func(err error) bool {
			var te *TransportError
			return errors.As(err, &te) && te.StatusCode == http.StatusTeapot
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, tc.status, jsendError{Status: "error", Message: "nope"})
			})
			var o out
			err := c.Invoke(context.Background(), "m", &in{}, &o, CallOpts{})
			if !tc.check(err) {
				t.Fatalf("unexpected error %#v", err)
			}
		})
	}
}

func TestForwardHeaders(t *testing.T) {
	got := make(chan string, 1)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Request-Id")
		writeJSON(w, http.StatusOK, out{V: 1})
	}, WithForwardHeaders("X-Request-Id"))

	incoming := http.Header{}
	incoming.Set("X-Request-Id", "abc-123")
	ctx := withMetadata(context.Background(), incoming, http.Header{})

	var o out
	if err := c.Invoke(ctx, "m", &in{}, &o, CallOpts{}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if v := <-got; v != "abc-123" {
		t.Fatalf("want forwarded request id, got %q", v)
	}
}

func TestMethodTimeout(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}, WithMethodTimeout("slow", 20*time.Millisecond))

	var o out
	err := c.Invoke(context.Background(), "slow", &in{}, &o, CallOpts{})
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want TransportError, got %v", err)
	}
}

func TestInterceptorSeesOneLogicalCall(t *testing.T) {
	var seen int
	count := func(info CallInfo, next Caller) Caller {
		return func(ctx context.Context, i any, o any) error {
			seen++
			return next(ctx, i, o)
		}
	}
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}, WithClientInterceptor(count))

	var o out
	_ = c.Invoke(context.Background(), "m", &in{}, &o, CallOpts{Idempotent: true})
	if seen != 1 {
		t.Fatalf("interceptor ran %d times, want 1 (retries are inside)", seen)
	}
}

func TestEnvVarForService(t *testing.T) {
	if got := EnvVarForService("user-profiles"); got != "SERVICE_USER_PROFILES_URL" {
		t.Fatalf("got %q", got)
	}
}
