package codegen

import (
	"fmt"
	"sort"
	"strings"
)

// Профиль rpc — ограничение, которого в OpenAPI нет. Без линта оно не
// существует, поэтому gen без успешного Lint не запускается.

type LintError struct {
	File string
	Msg  string
}

func (e LintError) Error() string { return e.File + ": " + e.Msg }

type Lint struct {
	spec   *Spec
	errs   []LintError
	seenID map[string]bool
}

func LintSpec(s *Spec) []LintError {
	l := &Lint{spec: s, seenID: map[string]bool{}}
	l.checkRoot()
	l.checkPaths()
	l.checkSchemas()
	sort.Slice(l.errs, func(i, j int) bool { return l.errs[i].Msg < l.errs[j].Msg })
	return l.errs
}

func (l *Lint) errf(format string, args ...any) {
	l.errs = append(l.errs, LintError{File: l.spec.Path, Msg: fmt.Sprintf(format, args...)})
}

func (l *Lint) checkRoot() {
	if !strings.HasPrefix(l.spec.OpenAPI, "3.") {
		l.errf("openapi must be 3.x, got %q", l.spec.OpenAPI)
	}
	switch {
	case l.spec.Service == "":
		l.errf("x-babble-service is required (service name, e.g. \"users\")")
	case !IsServiceName(l.spec.Service):
		l.errf("x-babble-service %q must be lowercase with dashes", l.spec.Service)
	}
	if l.spec.Info.Version == "" {
		l.errf("info.version is required")
	}
	if len(l.spec.Paths) == 0 {
		l.errf("paths must declare at least one method")
	}
	if _, ok := l.spec.Components.Schemas[jsendErrorSchema]; !ok {
		l.errf("components.schemas.%s is required (error envelope for 400/500)", jsendErrorSchema)
	}
}

const jsendErrorSchema = "JSendError"

func (l *Lint) checkPaths() {
	paths := make([]string, 0, len(l.spec.Paths))
	for p := range l.spec.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		item := l.spec.Paths[path]
		if forbidden := item.forbidden(); len(forbidden) > 0 {
			l.errf("%s: only post is allowed, found: %s", path, strings.Join(forbidden, ", "))
		}
		if item.Post == nil {
			l.errf("%s: post is required", path)
			continue
		}
		l.checkOperation(path, item.Post)
	}
}

func (l *Lint) checkOperation(path string, op *Operation) {
	id := op.OperationID
	switch {
	case id == "":
		l.errf("%s: operationId is required", path)
		return
	case !IsLowerCamel(id):
		l.errf("%s: operationId %q must be lowerCamelCase", path, id)
	}
	if l.seenID[id] {
		l.errf("%s: operationId %q is not unique", path, id)
	}
	l.seenID[id] = true

	if want := "/" + id; path != want {
		l.errf("%s: path must be %q (equal to operationId)", path, want)
	}
	if len(op.Parameters) > 0 {
		l.errf("%s: parameters are not allowed (no path or query parameters in the rpc profile)", path)
	}

	method := ToPascal(id)
	l.checkRequestBody(path, method, op)
	l.checkResponses(path, method, op)
}

func (l *Lint) checkRequestBody(path, method string, op *Operation) {
	if op.RequestBody == nil {
		l.errf("%s: requestBody is required", path)
		return
	}
	if !op.RequestBody.Required {
		l.errf("%s: requestBody.required must be true", path)
	}
	l.checkContent(path, "requestBody", op.RequestBody.Content, method+"In")
}

func (l *Lint) checkResponses(path, method string, op *Operation) {
	want := map[string]bool{"200": true, "400": true, "500": true}
	for code := range op.Responses {
		if !want[code] {
			l.errf("%s: response %q is not allowed (only 200, 400, 500)", path, code)
		}
	}
	for _, code := range []string{"200", "400", "500"} {
		resp, ok := op.Responses[code]
		if !ok {
			l.errf("%s: response %s is required", path, code)
			continue
		}
		schema := method + "Out"
		if code != "200" {
			schema = jsendErrorSchema
		}
		l.checkContent(path, "response "+code, resp.Content, schema)
	}
}

func (l *Lint) checkContent(path, where string, content map[string]MediaType, wantSchema string) {
	if len(content) == 0 {
		l.errf("%s: %s: content is required", path, where)
		return
	}
	for ct := range content {
		if ct != "application/json" {
			l.errf("%s: %s: content type %q is not allowed (only application/json)", path, where, ct)
		}
	}
	mt, ok := content["application/json"]
	if !ok {
		return
	}
	if mt.Schema == nil {
		l.errf("%s: %s: schema is required", path, where)
		return
	}
	if mt.Schema.Ref == "" {
		l.errf("%s: %s: schema must be a $ref to components.schemas.%s (inline schemas are not allowed)", path, where, wantSchema)
		return
	}
	got := RefName(mt.Schema.Ref)
	if _, ok := l.spec.Components.Schemas[got]; !ok {
		l.errf("%s: %s: unknown $ref %q", path, where, mt.Schema.Ref)
		return
	}
	if got != wantSchema {
		l.errf("%s: %s: schema must be %s, got %s", path, where, wantSchema, got)
	}
}

func (l *Lint) checkSchemas() {
	for _, name := range l.spec.SchemaNames() {
		schema := l.spec.Components.Schemas[name]
		if !IsUpperCamel(name) {
			l.errf("schema %s: name must be UpperCamelCase", name)
		}
		l.checkSchema(name, schema)
	}
	l.checkRecursion()
}

func (l *Lint) checkSchema(name string, s *Schema) {
	if s == nil {
		l.errf("schema %s: is empty", name)
		return
	}
	if s.Type != "" && s.Type != "object" {
		l.errf("schema %s: top-level components must be objects, got %q", name, s.Type)
		return
	}
	if len(s.Properties) == 0 && s.AdditionalProperties == nil {
		l.errf("schema %s: must declare properties (empty messages are not allowed)", name)
		return
	}

	required := map[string]bool{}
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			l.errf("schema %s: required lists unknown property %q", name, r)
		}
		required[r] = true
	}

	props := make([]string, 0, len(s.Properties))
	for p := range s.Properties {
		props = append(props, p)
	}
	sort.Strings(props)

	for _, prop := range props {
		if !IsLowerCamel(prop) {
			l.errf("schema %s: property %q must be lowerCamelCase", name, prop)
		}
		if s.OneOf && required[prop] {
			l.errf("schema %s: x-babble-oneof variants must all be optional, %q is required", name, prop)
		}
		l.checkProperty(name, prop, s.Properties[prop])
	}
}

func (l *Lint) checkProperty(schema, prop string, p *Schema) {
	if p == nil {
		l.errf("schema %s: property %q is empty", schema, prop)
		return
	}
	if p.Ref != "" {
		if _, ok := l.spec.Components.Schemas[RefName(p.Ref)]; !ok {
			l.errf("schema %s: property %q references unknown schema %q", schema, prop, p.Ref)
		}
		return
	}
	for _, v := range p.Enum {
		if !IsUpperSnake(v) {
			l.errf("schema %s: property %q: enum value %q must be UPPER_SNAKE_CASE", schema, prop, v)
		}
	}
	if len(p.Enum) > 0 && p.Type != "" && p.Type != "string" {
		l.errf("schema %s: property %q: enum is only supported for strings", schema, prop)
	}
	if p.Type == "object" && p.AdditionalProperties == nil {
		l.errf("schema %s: property %q: inline objects are not allowed, use a $ref", schema, prop)
	}
	if p.Type == "array" {
		if p.Items == nil {
			l.errf("schema %s: property %q: array requires items", schema, prop)
		} else {
			l.checkProperty(schema, prop+"[]", p.Items)
		}
	}
	if p.AdditionalProperties != nil {
		l.checkProperty(schema, prop+"[*]", p.AdditionalProperties)
	}
}

// checkRecursion ищет цикл, проходящий только через обязательные поля: такая
// структура не может быть построена и не может быть провалидирована.
func (l *Lint) checkRecursion() {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}

	var walk func(name string, stack []string)
	walk = func(name string, stack []string) {
		switch color[name] {
		case grey:
			l.errf("schema %s: required fields form a cycle: %s", name, strings.Join(append(stack, name), " → "))
			return
		case black:
			return
		}
		color[name] = grey
		defer func() { color[name] = black }()

		s := l.spec.Components.Schemas[name]
		if s == nil {
			return
		}
		required := map[string]bool{}
		for _, r := range s.Required {
			required[r] = true
		}
		names := make([]string, 0, len(s.Properties))
		for p := range s.Properties {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, prop := range names {
			p := s.Properties[prop]
			// Необязательное поле — указатель, цикл через него разрывается.
			// Массивы и карты тоже разрывают: они могут быть пустыми.
			if p == nil || !required[prop] || p.Ref == "" {
				continue
			}
			walk(RefName(p.Ref), append(stack, name))
		}
	}

	for _, name := range l.spec.SchemaNames() {
		if color[name] == white {
			walk(name, nil)
		}
	}
}
