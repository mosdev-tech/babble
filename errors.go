package babble

import (
	"errors"
	"fmt"
)

// Транспортный уровень знает ровно три исхода: успех, ValidationError (клиент
// нарушил контракт) и ServerError (сервер не смог). Бизнес-ошибки в транспорт
// не выносятся — они описываются в схеме ответа.

// Классы ошибок, которые проставляет сам рантайм. Kind — открытое множество:
// сервис вправе завести свои значения через ValidationError.WithKind.
const (
	KindValidation       = "validation"
	KindNotFound         = "not_found"
	KindMethodNotAllowed = "method_not_allowed"
)

// ValidationError — клиент нарушил контракт: невалидный JSON, нарушено
// ограничение схемы, несуществующий идентификатор. Отдаётся как HTTP 400.
type ValidationError struct {
	// Field — путь поля, на котором сломалась валидация ("items[3].role").
	// Пустой, если ошибка не привязана к полю.
	Field string
	// Kind — машиночитаемый класс ошибки; уезжает в тело как error.kind.
	// Пустой означает KindValidation.
	Kind string
	Msg  string
}

func NewValidationError(format string, args ...any) *ValidationError {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// WithKind проставляет класс ошибки, отличный от validation:
//
//	return nil, babble.NewValidationError("car with id %d does not exist", id).
//		WithKind("not_found")
func (e *ValidationError) WithKind(kind string) *ValidationError {
	e.Kind = kind
	return e
}

// KindOr возвращает Kind, подставляя KindValidation вместо пустого.
func (e *ValidationError) KindOr() string {
	if e.Kind == "" {
		return KindValidation
	}
	return e.Kind
}

func newFieldError(path, format string, args ...any) *ValidationError {
	return &ValidationError{Field: path, Msg: fmt.Sprintf(format, args...)}
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

// ServerError — сервер не может выполнить запрос: БД недоступна, зависимость не
// ответила, внутренний сбой. Отдаётся как HTTP 500; исходная ошибка уходит в
// лог и наружу не утекает.
type ServerError struct {
	Msg string
	Err error
}

func NewServerError(err error) *ServerError {
	return &ServerError{Msg: "internal server error", Err: err}
}

func (e *ServerError) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	return e.Msg + ": " + e.Err.Error()
}

func (e *ServerError) Unwrap() error { return e.Err }

// TransportError — клиентская сторона получила то, чего в контракте нет:
// неожиданный код ответа или тело, которое не разбирается. Молчаливого
// «считаем, что ок» нет.
type TransportError struct {
	Procedure  string
	StatusCode int
	Body       string
	Err        error
}

func (e *TransportError) Error() string {
	switch {
	case e.Err != nil && e.StatusCode == 0:
		return fmt.Sprintf("babble: %s: %v", e.Procedure, e.Err)
	case e.Err != nil:
		return fmt.Sprintf("babble: %s: unexpected status %d: %v", e.Procedure, e.StatusCode, e.Err)
	default:
		return fmt.Sprintf("babble: %s: unexpected status %d: %s", e.Procedure, e.StatusCode, e.Body)
	}
}

func (e *TransportError) Unwrap() error { return e.Err }

// AsValidationError и AsServerError — сокращения для errors.As, чтобы
// вызывающему коду не приходилось объявлять переменную.
func AsValidationError(err error) (*ValidationError, bool) {
	var target *ValidationError
	ok := errors.As(err, &target)
	return target, ok
}

func AsServerError(err error) (*ServerError, bool) {
	var target *ServerError
	ok := errors.As(err, &target)
	return target, ok
}

// errorBody — единый формат тела ошибки на проводе:
//
//	400 {"error": {"kind": "validation", "message": "..."}}
//	500 {"error": {"message": "internal server error"}}
//
// kind есть только там, где он что-то значит для вызывающего: на 500 наружу не
// выносится ничего, кроме сообщения.
type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message"`
}

func validationBody(kind, msg string) errorBody {
	return errorBody{Error: errorPayload{Kind: kind, Message: msg}}
}

func serverBody(msg string) errorBody {
	return errorBody{Error: errorPayload{Message: msg}}
}

// maxErrorBodyBytes ограничивает объём чужого тела, который клиент утащит в
// TransportError: ошибочный ответ может оказаться HTML-страницей балансировщика.
const maxErrorBodyBytes = 4 << 10
