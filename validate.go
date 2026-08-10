package babble

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Validatable реализуется сгенерированными DTO. Обход рекурсивный: вложенные
// структуры, элементы массивов и значения карт; path накапливает путь поля,
// чтобы 400 был пригоден для отладки ("items[3].role").
type Validatable interface {
	Validate_(v *Visitor, path string) error
}

// Defaultable реализуется сгенерированными DTO, у которых в схеме есть
// default. Вызывается до валидации.
type Defaultable interface {
	FillDefault_(v *Visitor)
}

// Visitor защищает рекурсивный обход от циклов в графе DTO.
type Visitor struct {
	seen map[any]struct{}
}

func NewVisitor() *Visitor { return &Visitor{seen: make(map[any]struct{}, 8)} }

// Enter возвращает false, если по этому указателю обход уже проходил — значит
// в графе цикл и спускаться повторно не нужно.
func (v *Visitor) Enter(p any) bool {
	if v == nil || p == nil {
		return true
	}
	if v.seen == nil {
		v.seen = make(map[any]struct{}, 8)
	}
	if _, ok := v.seen[p]; ok {
		return false
	}
	v.seen[p] = struct{}{}
	return true
}

// Validate прогоняет FillDefault_ и Validate_, если значение их реализует.
// Значения без сгенерированных методов проходят без проверок.
func Validate(value any) error {
	if d, ok := value.(Defaultable); ok {
		d.FillDefault_(NewVisitor())
	}
	if v, ok := value.(Validatable); ok {
		return v.Validate_(NewVisitor(), "")
	}
	return nil
}

// Field склеивает путь поля для сообщений об ошибках.
func Field(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// Index склеивает путь элемента массива или значения карты.
func Index(path string, i int) string {
	return fmt.Sprintf("%s[%d]", path, i)
}

// Key склеивает путь значения карты по строковому ключу.
func Key(path, k string) string {
	return fmt.Sprintf("%s[%q]", path, k)
}

// ---------- ограничения схемы ----------

func CheckRequired(path string, present bool) error {
	if !present {
		return newFieldError(path, "is required")
	}
	return nil
}

func CheckEnum[T ~string](path string, value T, allowed []T) error {
	for _, a := range allowed {
		if a == value {
			return nil
		}
	}
	strs := make([]string, len(allowed))
	for i, a := range allowed {
		strs[i] = string(a)
	}
	return newFieldError(path, "value %q is not one of [%s]", string(value), strings.Join(strs, ", "))
}

func CheckMinLength(path, value string, min int) error {
	if n := utf8.RuneCountInString(value); n < min {
		return newFieldError(path, "length %d is less than minimum %d", n, min)
	}
	return nil
}

func CheckMaxLength(path, value string, max int) error {
	if n := utf8.RuneCountInString(value); n > max {
		return newFieldError(path, "length %d exceeds maximum %d", n, max)
	}
	return nil
}

func CheckPattern(path, value, pattern string) error {
	re, err := compilePattern(pattern)
	if err != nil {
		return newFieldError(path, "invalid pattern %q in schema: %v", pattern, err)
	}
	if !re.MatchString(value) {
		return newFieldError(path, "value %q does not match pattern %q", value, pattern)
	}
	return nil
}

type number interface {
	~int | ~int32 | ~int64 | ~float32 | ~float64
}

func CheckMinimum[T number](path string, value, min T) error {
	if value < min {
		return newFieldError(path, "value %v is less than minimum %v", value, min)
	}
	return nil
}

func CheckMaximum[T number](path string, value, max T) error {
	if value > max {
		return newFieldError(path, "value %v exceeds maximum %v", value, max)
	}
	return nil
}

func CheckMinItems(path string, n, min int) error {
	if n < min {
		return newFieldError(path, "has %d items, minimum is %d", n, min)
	}
	return nil
}

func CheckMaxItems(path string, n, max int) error {
	if n > max {
		return newFieldError(path, "has %d items, maximum is %d", n, max)
	}
	return nil
}

// CheckOneOf проверяет инвариант x-babble-oneof: непустым может быть не больше
// одного варианта. names перечисляет имена заполненных полей.
func CheckOneOf(path string, names []string) error {
	if len(names) > 1 {
		return newFieldError(path, "exactly one variant may be set, got %d: %s", len(names), strings.Join(names, ", "))
	}
	return nil
}

var (
	patternsMu sync.RWMutex
	patterns   = map[string]*regexp.Regexp{}
)

func compilePattern(p string) (*regexp.Regexp, error) {
	patternsMu.RLock()
	re, ok := patterns[p]
	patternsMu.RUnlock()
	if ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	patternsMu.Lock()
	patterns[p] = re
	patternsMu.Unlock()
	return re, nil
}
