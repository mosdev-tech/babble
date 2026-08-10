package babble

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var echo = Method[in, out]{Name: "echo"}

func serve(t *testing.T, opts ...ServerOption) *httptest.Server {
	t.Helper()
	srv, err := NewServer(opts...)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestEchoRoundTrip(t *testing.T) {
	ts := serve(t, WithMethod(echo, func(_ context.Context, i *in) (*out, error) {
		return &out{V: i.V + 1}, nil
	}))

	resp, err := http.Post(ts.URL+"/echo", "application/json", strings.NewReader(`{"v":41}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var o out
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if o.V != 42 {
		t.Fatalf("got %d, want 42", o.V)
	}
}

func TestDuplicateMethodIsAnError(t *testing.T) {
	h := func(_ context.Context, i *in) (*out, error) { return &out{}, nil }
	_, err := NewServer(WithMethod(echo, h), WithMethod(echo, h))
	if err == nil {
		t.Fatal("want error on duplicate registration")
	}
}

// Все ответы, включая 404/405, обязаны быть JSON — иначе клиент получает
// HTML балансировщика вместо контракта.
func TestNotFoundAndMethodNotAllowedAreJSON(t *testing.T) {
	ts := serve(t, WithMethod(echo, func(_ context.Context, i *in) (*out, error) { return &out{}, nil }))

	for _, tc := range []struct {
		name string
		do   func() (*http.Response, error)
		code int
	}{
		{"unknown", func() (*http.Response, error) { return http.Post(ts.URL+"/nope", "application/json", nil) }, http.StatusNotFound},
		{"wrong method", func() (*http.Response, error) { return http.Get(ts.URL + "/echo") }, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.do()
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.code {
				t.Fatalf("want %d, got %d", tc.code, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("want json, got %q", ct)
			}
			var body errorBody
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Error.Kind == "" || body.Error.Message == "" {
				t.Fatalf("unexpected body %+v", body)
			}
		})
	}
}

func TestPanicBecomesJSON500(t *testing.T) {
	var logged error
	ts := serve(t,
		WithMethod(echo, func(_ context.Context, i *in) (*out, error) { panic("boom") }),
		WithErrorLogger(func(_ context.Context, err error) { logged = err }),
	)

	resp, err := http.Post(ts.URL+"/echo", "application/json", strings.NewReader(`{"v":1}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	// На 500 наружу уходит только message: kind ничего не сообщил бы вызывающему.
	if got := strings.TrimSpace(string(raw)); got != `{"error":{"message":"internal server error"}}` {
		t.Fatalf("unexpected 500 body: %s", got)
	}
	if logged == nil {
		t.Fatal("panic was not logged")
	}
}

func TestHealthReadiness(t *testing.T) {
	failing := true
	ts := serve(t,
		WithMethod(echo, func(_ context.Context, i *in) (*out, error) { return &out{}, nil }),
		WithHealthCheck(func(context.Context) error {
			if failing {
				return io.ErrUnexpectedEOF
			}
			return nil
		}),
	)

	resp, err := http.Get(ts.URL + HealthPath)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 while the dependency is down, got %d", resp.StatusCode)
	}

	failing = false
	resp, err = http.Get(ts.URL + HealthPath)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestEmptyBodyIsValidationError(t *testing.T) {
	ts := serve(t, WithMethod(echo, func(_ context.Context, i *in) (*out, error) { return &out{}, nil }))

	resp, err := http.Post(ts.URL+"/echo", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestMetadataAndResponseHeader(t *testing.T) {
	ts := serve(t, WithMethod(echo, func(ctx context.Context, i *in) (*out, error) {
		SetResponseHeader(ctx, "X-Echo-Auth", Metadata(ctx).Get("Authorization"))
		return &out{V: i.V}, nil
	}))

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/echo", strings.NewReader(`{"v":1}`))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Echo-Auth"); got != "Bearer t" {
		t.Fatalf("metadata did not reach the handler, got %q", got)
	}
}

// 400 несёт kind: вызывающий должен уметь разобрать причину машинно, не
// разглядывая текст сообщения.
func TestValidationErrorCarriesKind(t *testing.T) {
	ts := serve(t, WithMethod(echo, func(context.Context, *in) (*out, error) {
		return nil, NewValidationError("car with id %d does not exist", 113218).WithKind("not_found")
	}))

	resp, err := http.Post(ts.URL+"/echo", "application/json", strings.NewReader(`{"v":1}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	want := `{"error":{"kind":"not_found","message":"car with id 113218 does not exist"}}`
	if got := strings.TrimSpace(string(raw)); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// Без явного kind ошибка валидации остаётся validation.
func TestSchemaViolationKindIsValidation(t *testing.T) {
	ts := serve(t, WithMethod(echo, func(context.Context, *in) (*out, error) { return &out{}, nil }))

	resp, err := http.Post(ts.URL+"/echo", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Kind != KindValidation {
		t.Fatalf("want kind %q, got %q", KindValidation, body.Error.Kind)
	}
}
