package babble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HealthPath — liveness/readiness проба. Регистрируется всегда и не проходит
// через авторизацию.
const HealthPath = "/health"

// Settings — общие настройки сервера. Нулевые поля заменяются дефолтами.
type Settings struct {
	Address         string
	ShutdownTimeout time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	// MaxRequestBytes ограничивает размер тела запроса; 0 — дефолт 4 МиБ.
	MaxRequestBytes int64
}

func (s Settings) withDefaults() Settings {
	if s.Address == "" {
		s.Address = ":8080"
	}
	if s.ShutdownTimeout == 0 {
		s.ShutdownTimeout = 10 * time.Second
	}
	if s.ReadTimeout == 0 {
		s.ReadTimeout = 10 * time.Second
	}
	if s.WriteTimeout == 0 {
		s.WriteTimeout = 30 * time.Second
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = 60 * time.Second
	}
	if s.MaxRequestBytes == 0 {
		s.MaxRequestBytes = 4 << 20
	}
	return s
}

// MethodDescriptor — то, что кодоген знает о методе из контракта.
type MethodDescriptor struct {
	Name       string
	Idempotent bool
	Public     bool
}

// Method — типизированное описание метода, которое генерирует кодоген:
//
//	var Create = babble.Method[dto.CreateIn, dto.CreateOut]{Name: "create"}
//
// Параметры In/Out выводятся в WithMethod из бизнес-хендлера, поэтому
// несовпадение сигнатуры с контрактом не компилируется.
type Method[In, Out any] struct {
	Name       string
	Idempotent bool
	Public     bool
}

func (m Method[In, Out]) Descriptor() MethodDescriptor {
	return MethodDescriptor{Name: m.Name, Idempotent: m.Idempotent, Public: m.Public}
}

// ServerHandler — вызов метода после декодирования и валидации входа.
type ServerHandler func(ctx context.Context, in any) (any, error)

// ServerInterceptor — типизированный уровень расширения: логирование, метрики,
// авторизация (дескриптор несёт Public из контракта). Выполняется после
// маршрутизации и декода.
type ServerInterceptor func(desc MethodDescriptor, next ServerHandler) ServerHandler

// ErrorLogger получает ошибки, которые наружу отдаются как 500.
type ErrorLogger func(ctx context.Context, err error)

type registration struct {
	desc    MethodDescriptor
	newIn   func() any
	handler ServerHandler
}

type serverOptions struct {
	settings     Settings
	methods      map[string]registration
	health       func(context.Context) error
	middleware   []func(http.Handler) http.Handler
	interceptors []ServerInterceptor
	errorLog     ErrorLogger
	err          error
}

type ServerOption func(*serverOptions)

func WithSettings(s Settings) ServerOption {
	return func(o *serverOptions) { o.settings = s }
}

// WithMethod связывает сгенерированное описание метода с бизнес-хендлером.
func WithMethod[In, Out any](m Method[In, Out], fn func(context.Context, *In) (*Out, error)) ServerOption {
	return func(o *serverOptions) {
		if m.Name == "" {
			o.err = errors.Join(o.err, errors.New("babble: method with empty name"))
			return
		}
		if _, dup := o.methods[m.Name]; dup {
			o.err = errors.Join(o.err, fmt.Errorf("babble: method %q registered twice", m.Name))
			return
		}
		o.methods[m.Name] = registration{
			desc:  m.Descriptor(),
			newIn: func() any { return new(In) },
			handler: func(ctx context.Context, in any) (any, error) {
				typed, ok := in.(*In)
				if !ok {
					return nil, fmt.Errorf("babble: method %q got %T, want %T", m.Name, in, (*In)(nil))
				}
				out, err := fn(ctx, typed)
				if err != nil {
					return nil, err
				}
				if out == nil {
					return nil, fmt.Errorf("babble: method %q returned nil output without error", m.Name)
				}
				return out, nil
			},
		}
	}
}

// WithHealthCheck превращает GET /health из liveness в readiness: ошибка →
// 503 {"status":"not_serving"}.
func WithHealthCheck(fn func(context.Context) error) ServerOption {
	return func(o *serverOptions) { o.health = fn }
}

// WithHTTPMiddleware — сырой уровень расширения, до маршрутизации: CORS,
// request-id, аутентификация, отклонение до чтения тела.
func WithHTTPMiddleware(mw func(http.Handler) http.Handler) ServerOption {
	return func(o *serverOptions) { o.middleware = append(o.middleware, mw) }
}

func WithServerInterceptor(i ServerInterceptor) ServerOption {
	return func(o *serverOptions) { o.interceptors = append(o.interceptors, i) }
}

func WithErrorLogger(l ErrorLogger) ServerOption {
	return func(o *serverOptions) { o.errorLog = l }
}

type Server struct {
	settings Settings
	methods  map[string]registration
	health   func(context.Context) error
	errorLog ErrorLogger
	handler  http.Handler
	maxBytes int64
}

func NewServer(opts ...ServerOption) (*Server, error) {
	o := &serverOptions{methods: map[string]registration{}}
	for _, opt := range opts {
		opt(o)
	}
	if o.err != nil {
		return nil, o.err
	}
	settings := o.settings.withDefaults()

	// Интерсепторы навешиваются один раз при сборке: первый добавленный —
	// самый внешний.
	for name, reg := range o.methods {
		h := reg.handler
		for i := len(o.interceptors) - 1; i >= 0; i-- {
			h = o.interceptors[i](reg.desc, h)
		}
		reg.handler = h
		o.methods[name] = reg
	}

	s := &Server{
		settings: settings,
		methods:  o.methods,
		health:   o.health,
		errorLog: o.errorLog,
		maxBytes: settings.MaxRequestBytes,
	}

	var h http.Handler = http.HandlerFunc(s.serveHTTP)
	for i := len(o.middleware) - 1; i >= 0; i-- {
		h = o.middleware[i](h)
	}
	// recover — снаружи от пользовательского middleware, чтобы паника в нём
	// тоже отдавала JSON.
	s.handler = s.recoverer(h)
	return s, nil
}

// Handler отдаёт http.Handler — так тесты бьют по серверу через httptest без
// сети.
func (s *Server) Handler() http.Handler { return s.handler }

// Methods перечисляет зарегистрированные методы (для служебных нужд: реестры,
// проверки конфигурации).
func (s *Server) Methods() []MethodDescriptor {
	out := make([]MethodDescriptor, 0, len(s.methods))
	for _, reg := range s.methods {
		out = append(out, reg.desc)
	}
	return out
}

// Run поднимает сервер и глушит его по отмене ctx.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.settings.Address,
		Handler:      s.handler,
		ReadTimeout:  s.settings.ReadTimeout,
		WriteTimeout: s.settings.WriteTimeout,
		IdleTimeout:  s.settings.IdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.settings.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	}
}

// ---------- маршрутизация ----------
//
// Маршруты — точное совпадение пути (профиль rpc: путь ровно /{operationId}),
// поэтому вместо ServeMux используется прямой поиск по карте: он даёт полный
// контроль над 404/405, которые тоже обязаны отдавать конверт ошибки.

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == HealthPath {
		s.serveHealth(w, r)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/")
	reg, ok := s.methods[name]
	if !ok || strings.Contains(name, "/") {
		writeJSON(w, http.StatusNotFound, validationBody(KindNotFound, "not found"))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, validationBody(KindMethodNotAllowed, "method not allowed"))
		return
	}

	ctx := withMetadata(r.Context(), r.Header, w.Header())

	in := reg.newIn()
	if err := s.decode(r, in); err != nil {
		s.writeError(ctx, w, err)
		return
	}
	if err := Validate(in); err != nil {
		s.writeError(ctx, w, err)
		return
	}

	out, err := reg.handler(ctx, in)
	if err != nil {
		s.writeError(ctx, w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) decode(r *http.Request, dst any) error {
	body := http.MaxBytesReader(nil, r.Body, s.maxBytes)
	defer body.Close()

	raw, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return NewValidationError("request body exceeds %d bytes", s.maxBytes)
		}
		return NewValidationError("cannot read request body: %v", err)
	}
	if len(raw) == 0 {
		return NewValidationError("request body is required")
	}
	// Неизвестные поля игнорируются намеренно: иначе выкатка нового поля у
	// вызывающего ломает ещё не обновлённый сервер.
	if err := json.Unmarshal(raw, dst); err != nil {
		return NewValidationError("cannot decode request body: %v", err)
	}
	return nil
}

func (s *Server) serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, validationBody(KindMethodNotAllowed, "method not allowed"))
		return
	}
	if s.health != nil {
		if err := s.health(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "not_serving",
				"error":  err.Error(),
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "serving"})
}

func (s *Server) writeError(ctx context.Context, w http.ResponseWriter, err error) {
	if ve, ok := AsValidationError(err); ok {
		writeJSON(w, http.StatusBadRequest, validationBody(ve.KindOr(), ve.Error()))
		return
	}
	if s.errorLog != nil {
		s.errorLog(ctx, err)
	}
	writeJSON(w, http.StatusInternalServerError, serverBody("internal server error"))
}

// recoverer отдаёт JSON при панике: стандартный net/http просто рвёт
// соединение, а весь API обязан говорить одним форматом.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil || rec == http.ErrAbortHandler {
				if rec != nil {
					panic(rec)
				}
				return
			}
			if s.errorLog != nil {
				s.errorLog(r.Context(), fmt.Errorf("panic recovered: %v", rec))
			}
			writeJSON(w, http.StatusInternalServerError, serverBody("internal server error"))
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
