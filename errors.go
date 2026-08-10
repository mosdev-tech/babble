package babble

import (
	"errors"
	"fmt"
)

// Транспортный уровень знает ровно три исхода: успех, ValidationError (клиент
// нарушил контракт) и ServerError (сервер не смог). Бизнес-ошибки в транспорт
// не выносятся — они описываются в схеме ответа.

// ValidationError — клиент нарушил контракт: невалидный JSON, нарушено
// ограничение схемы, несуществующий идентификатор. Отдаётся как HTTP 400.
type ValidationError struct {
	// Field — путь поля, на котором сломалась валидация ("items[3].role").
	// Пустой, если ошибка не привязана к полю.
	Field string
	Msg   string
}

func NewValidationError(format string, args ...any) *ValidationError {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
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

// jsendError — единый формат тела ошибки на проводе.
type jsendError struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// maxErrorBodyBytes ограничивает объём чужого тела, который клиент утащит в
// TransportError: ошибочный ответ может оказаться HTML-страницей балансировщика.
const maxErrorBodyBytes = 4 << 10
