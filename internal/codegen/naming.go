package codegen

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap   = regexp.MustCompile("([a-z0-9])([A-Z])")
	splitNonAlnum = regexp.MustCompile(`[_\-. ]+`)
)

// ToSnake: CreateUser → create_user. Имя пакета стаба и директории.
func ToSnake(s string) string {
	out := matchFirstCap.ReplaceAllString(s, "${1}_${2}")
	out = matchAllCap.ReplaceAllString(out, "${1}_${2}")
	out = splitNonAlnum.ReplaceAllString(out, "_")
	return strings.ToLower(out)
}

// ToPascal: create_user → CreateUser.
func ToPascal(s string) string {
	var b strings.Builder
	for _, part := range splitNonAlnum.Split(s, -1) {
		if part == "" {
			continue
		}
		r := []rune(part)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// ToCamel: CreateUser → createUser.
func ToCamel(s string) string {
	p := ToPascal(s)
	if p == "" {
		return p
	}
	r := []rune(p)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// PackageName делает из имени сервиса допустимое имя Go-пакета:
// user-profiles → userprofiles.
func PackageName(service string) string {
	var b strings.Builder
	for _, r := range service {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	name := b.String()
	if name == "" || unicode.IsDigit(rune(name[0])) {
		name = "svc" + name
	}
	return name
}

var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// SafeIdent защищает от коллизии имени пакета с ключевым словом Go.
func SafeIdent(s string) string {
	if goKeywords[s] {
		return s + "_"
	}
	return s
}

var (
	lowerCamelRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
	upperCamelRe = regexp.MustCompile(`^[A-Z][a-zA-Z0-9]*$`)
	upperSnakeRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	serviceRe    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

func IsLowerCamel(s string) bool  { return lowerCamelRe.MatchString(s) }
func IsUpperCamel(s string) bool  { return upperCamelRe.MatchString(s) }
func IsUpperSnake(s string) bool  { return upperSnakeRe.MatchString(s) }
func IsServiceName(s string) bool { return serviceRe.MatchString(s) }
