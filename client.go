package babble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"time"
)

// CallOpts — свойства метода, приезжающие из контракта через кодоген.
type CallOpts struct {
	Idempotent bool
}

// CallInfo — то, что видит интерсептор о вызове.
type CallInfo struct {
	Procedure  string
	Idempotent bool
}

// Caller — один вызов метода. Интерсепторы оборачивают именно его.
type Caller func(ctx context.Context, in any, out any) error

// ClientInterceptor — единственная точка расширения клиента: логирование,
// метрики, трассировка. Видит один логический вызов, ретраи внутри него.
type ClientInterceptor func(info CallInfo, next Caller) Caller

// Client — транспорт, поверх которого работают сгенерированные клиенты.
type Client interface {
	Invoke(ctx context.Context, procedure string, in any, out any, opts CallOpts) error
}

// Call — то, во что кодоген разворачивает метод клиента. Без рефлексии:
// несовпадение типов ловит компилятор.
func Call[In, Out any](ctx context.Context, c Client, procedure string, in *In, opts CallOpts) (*Out, error) {
	var out Out
	if err := c.Invoke(ctx, procedure, in, &out, opts); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetryPolicy применяется только к идемпотентным методам.
type RetryPolicy struct {
	// Attempts — общее число попыток, включая первую. 0/1 — без ретраев.
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

var DefaultRetryPolicy = RetryPolicy{Attempts: 3, BaseDelay: 50 * time.Millisecond, MaxDelay: time.Second}

// RecommendedClientSettings собирается один раз и передаётся каждому клиенту.
type RecommendedClientSettings struct {
	Timeout        time.Duration
	Retry          *RetryPolicy
	Interceptors   []ClientInterceptor
	ForwardHeaders []string
	HTTPClient     *http.Client
}

type clientOptions struct {
	baseURL        string
	httpClient     *http.Client
	timeout        time.Duration
	methodTimeout  map[string]time.Duration
	retry          RetryPolicy
	interceptors   []ClientInterceptor
	headers        [][2]string
	forwardHeaders []string
	err            error
}

type ClientOption func(*clientOptions)

func WithBaseURL(u string) ClientOption {
	return func(o *clientOptions) { o.baseURL = strings.TrimRight(u, "/") }
}

// WithBaseURLFromEnv резолвит адрес по конвенции SERVICE_<UPPER(name)>_URL.
func WithBaseURLFromEnv(service string) ClientOption {
	return func(o *clientOptions) {
		key := EnvVarForService(service)
		v := os.Getenv(key)
		if v == "" {
			o.err = errors.Join(o.err, fmt.Errorf("babble: %s is not set (base URL for service %q)", key, service))
			return
		}
		o.baseURL = strings.TrimRight(v, "/")
	}
}

// EnvVarForService — имя переменной окружения с адресом сервиса.
func EnvVarForService(service string) string {
	return "SERVICE_" + strings.ToUpper(strings.ReplaceAll(service, "-", "_")) + "_URL"
}

func WithHTTPClient(c *http.Client) ClientOption {
	return func(o *clientOptions) { o.httpClient = c }
}

// WithTimeout — общий потолок на вызов. Отмена ctx работает всегда и
// независимо; таймаут лишь ставит верхнюю границу.
func WithTimeout(d time.Duration) ClientOption {
	return func(o *clientOptions) { o.timeout = d }
}

func WithMethodTimeout(procedure string, d time.Duration) ClientOption {
	return func(o *clientOptions) {
		if o.methodTimeout == nil {
			o.methodTimeout = map[string]time.Duration{}
		}
		o.methodTimeout[procedure] = d
	}
}

func WithRetry(p RetryPolicy) ClientOption {
	return func(o *clientOptions) { o.retry = p }
}

func WithClientInterceptor(i ClientInterceptor) ClientOption {
	return func(o *clientOptions) { o.interceptors = append(o.interceptors, i) }
}

// WithHeader добавляет статический заголовок ко всем исходящим запросам.
func WithHeader(key, value string) ClientOption {
	return func(o *clientOptions) { o.headers = append(o.headers, [2]string{key, value}) }
}

// WithForwardHeaders пробрасывает перечисленные заголовки входящего запроса
// (request-id, авторизация, трассировка) в исходящий вызов.
func WithForwardHeaders(names ...string) ClientOption {
	return func(o *clientOptions) { o.forwardHeaders = append(o.forwardHeaders, names...) }
}

func WithRecommendedClientSettings(s RecommendedClientSettings) ClientOption {
	return func(o *clientOptions) {
		if s.Timeout != 0 {
			o.timeout = s.Timeout
		}
		if s.Retry != nil {
			o.retry = *s.Retry
		}
		if s.HTTPClient != nil {
			o.httpClient = s.HTTPClient
		}
		o.interceptors = append(o.interceptors, s.Interceptors...)
		o.forwardHeaders = append(o.forwardHeaders, s.ForwardHeaders...)
	}
}

// ServiceNamer реализуется сгенерированным типом Service клиента — по нему
// ClientFor резолвит имя сервиса.
type ServiceNamer interface {
	ServiceName__() string
}

// ClientFor собирает транспорт к сервису, определяя его имя по
// сгенерированному типу: babble.ClientFor[users.Service](opt).
func ClientFor[S ServiceNamer](opts ...ClientOption) (Client, error) {
	var s S
	all := append([]ClientOption{WithBaseURLFromEnv(s.ServiceName__())}, opts...)
	return NewClient(all...)
}

type client struct {
	baseURL        string
	httpClient     *http.Client
	timeout        time.Duration
	methodTimeout  map[string]time.Duration
	retry          RetryPolicy
	interceptors   []ClientInterceptor
	headers        [][2]string
	forwardHeaders []string
}

func NewClient(opts ...ClientOption) (Client, error) {
	o := &clientOptions{retry: DefaultRetryPolicy}
	for _, opt := range opts {
		opt(o)
	}
	if o.err != nil {
		return nil, o.err
	}
	if o.baseURL == "" {
		return nil, errors.New("babble: base URL is not set")
	}
	httpClient := o.httpClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &client{
		baseURL:        o.baseURL,
		httpClient:     httpClient,
		timeout:        o.timeout,
		methodTimeout:  o.methodTimeout,
		retry:          o.retry,
		interceptors:   o.interceptors,
		headers:        o.headers,
		forwardHeaders: o.forwardHeaders,
	}, nil
}

func (c *client) Invoke(ctx context.Context, procedure string, in any, out any, opts CallOpts) error {
	info := CallInfo{Procedure: procedure, Idempotent: opts.Idempotent}

	caller := Caller(func(ctx context.Context, in any, out any) error {
		return c.do(ctx, info, in, out)
	})
	for i := len(c.interceptors) - 1; i >= 0; i-- {
		caller = c.interceptors[i](info, caller)
	}

	if d := c.timeoutFor(procedure); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	return caller(ctx, in, out)
}

func (c *client) timeoutFor(procedure string) time.Duration {
	if d, ok := c.methodTimeout[procedure]; ok {
		return d
	}
	return c.timeout
}

func (c *client) do(ctx context.Context, info CallInfo, in any, out any) error {
	// Валидируем локально, чтобы падать здесь, а не по сети.
	if err := Validate(in); err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return &TransportError{Procedure: info.Procedure, Err: fmt.Errorf("cannot encode request: %w", err)}
	}

	attempts := 1
	if info.Idempotent && c.retry.Attempts > 1 {
		attempts = c.retry.Attempts
	}

	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.backoff(attempt)); err != nil {
				return err
			}
		}
		lastErr = c.attempt(ctx, info, body, out)
		if lastErr == nil {
			return nil
		}
		if !retryable(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

func (c *client) attempt(ctx context.Context, info CallInfo, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+info.Procedure, bytes.NewReader(body))
	if err != nil {
		return &TransportError{Procedure: info.Procedure, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for _, h := range c.headers {
		req.Header.Set(h[0], h[1])
	}
	if len(c.forwardHeaders) > 0 {
		md := Metadata(ctx)
		for _, name := range c.forwardHeaders {
			if v := md.Get(name); v != "" {
				req.Header.Set(name, v)
			}
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Ошибка транспорта: ответ не получен, повтор безопасен для
		// идемпотентного метода.
		return &TransportError{Procedure: info.Procedure, Err: err}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return &TransportError{
				Procedure:  info.Procedure,
				StatusCode: resp.StatusCode,
				Err:        fmt.Errorf("cannot decode response: %w", err),
			}
		}
		return nil
	case http.StatusBadRequest:
		return &ValidationError{Msg: c.errMessage(resp, "bad request")}
	case http.StatusInternalServerError:
		return &ServerError{Msg: c.errMessage(resp, "internal server error")}
	default:
		return &TransportError{
			Procedure:  info.Procedure,
			StatusCode: resp.StatusCode,
			Body:       readCapped(resp.Body),
		}
	}
}

func (c *client) errMessage(resp *http.Response, fallback string) string {
	var e jsendError
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBodyBytes)).Decode(&e); err != nil || e.Message == "" {
		return fallback
	}
	return e.Message
}

// retryable: повторяем только то, что заведомо не выполнилось или явно просит
// повторить. 500 не повторяем никогда — сервер мог начать выполнять и упасть.
func retryable(err error) bool {
	var te *TransportError
	if !errors.As(err, &te) {
		return false
	}
	if te.StatusCode == 0 {
		return true
	}
	return te.StatusCode == http.StatusTooManyRequests || te.StatusCode == http.StatusServiceUnavailable
}

func (c *client) backoff(attempt int) time.Duration {
	base := c.retry.BaseDelay
	if base <= 0 {
		base = DefaultRetryPolicy.BaseDelay
	}
	max := c.retry.MaxDelay
	if max <= 0 {
		max = DefaultRetryPolicy.MaxDelay
	}
	d := base << (attempt - 1)
	if d > max {
		d = max
	}
	// Джиттер: половина фиксированная, половина случайная.
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func readCapped(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	return string(b)
}
