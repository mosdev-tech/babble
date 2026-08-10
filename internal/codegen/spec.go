package codegen

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Подмножество OpenAPI 3.0, которое понимает профиль rpc. Всё, что сюда не
// попало, отсекается линтом, а не игнорируется молча.

type Spec struct {
	OpenAPI    string              `yaml:"openapi"`
	Info       Info                `yaml:"info"`
	Service    string              `yaml:"x-babble-service"`
	Paths      map[string]PathItem `yaml:"paths"`
	Components Components          `yaml:"components"`

	// Path — откуда прочитан контракт; нужен для сообщений линта.
	Path string `yaml:"-"`
}

type Info struct {
	Title   string `yaml:"title"`
	Version string `yaml:"version"`
}

type Components struct {
	Schemas map[string]*Schema `yaml:"schemas"`
}

type PathItem struct {
	Post *Operation `yaml:"post"`

	// Остальные методы профилем запрещены; поля нужны, чтобы линт мог о них
	// сообщить, а не проигнорировать.
	Get     *Operation `yaml:"get"`
	Put     *Operation `yaml:"put"`
	Delete  *Operation `yaml:"delete"`
	Patch   *Operation `yaml:"patch"`
	Head    *Operation `yaml:"head"`
	Options *Operation `yaml:"options"`
}

func (p PathItem) forbidden() []string {
	var out []string
	for name, op := range map[string]*Operation{
		"get": p.Get, "put": p.Put, "delete": p.Delete,
		"patch": p.Patch, "head": p.Head, "options": p.Options,
	} {
		if op != nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

type Operation struct {
	OperationID string              `yaml:"operationId"`
	Summary     string              `yaml:"summary"`
	Description string              `yaml:"description"`
	Parameters  []yaml.Node         `yaml:"parameters"`
	RequestBody *RequestBody        `yaml:"requestBody"`
	Responses   map[string]Response `yaml:"responses"`
	Idempotent  bool                `yaml:"x-babble-idempotent"`
	Public      bool                `yaml:"x-babble-public"`
}

type RequestBody struct {
	Required bool                 `yaml:"required"`
	Content  map[string]MediaType `yaml:"content"`
}

type Response struct {
	Description string               `yaml:"description"`
	Content     map[string]MediaType `yaml:"content"`
}

type MediaType struct {
	Schema *Schema `yaml:"schema"`
}

type Schema struct {
	Type                 string             `yaml:"type"`
	Format               string             `yaml:"format"`
	Ref                  string             `yaml:"$ref"`
	Properties           map[string]*Schema `yaml:"properties"`
	Items                *Schema            `yaml:"items"`
	AdditionalProperties *Schema            `yaml:"additionalProperties"`
	Required             []string           `yaml:"required"`
	Enum                 []string           `yaml:"enum"`
	Default              any                `yaml:"default"`
	Description          string             `yaml:"description"`
	Nullable             bool               `yaml:"nullable"`

	MinLength *int     `yaml:"minLength"`
	MaxLength *int     `yaml:"maxLength"`
	Pattern   string   `yaml:"pattern"`
	Minimum   *float64 `yaml:"minimum"`
	Maximum   *float64 `yaml:"maximum"`
	MinItems  *int     `yaml:"minItems"`
	MaxItems  *int     `yaml:"maxItems"`

	OneOf bool `yaml:"x-babble-oneof"`
}

// knownExtensions — белый список; всё остальное с префиксом x- линт считает
// опечаткой, потому что молча проигнорированное расширение хуже отсутствующего.
var knownExtensions = map[string]bool{
	"x-babble-service":    true,
	"x-babble-idempotent": true,
	"x-babble-public":     true,
	"x-babble-oneof":      true,
}

// Method — операция вместе с её путём, отсортированные детерминированно.
type Method struct {
	Path      string
	Operation *Operation
}

// Methods возвращает операции в стабильном порядке (по operationId).
func (s *Spec) Methods() []Method {
	out := make([]Method, 0, len(s.Paths))
	for path, item := range s.Paths {
		if item.Post != nil {
			out = append(out, Method{Path: path, Operation: item.Post})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Operation.OperationID < out[j].Operation.OperationID
	})
	return out
}

// SchemaNames — имена компонентов в стабильном порядке.
func (s *Spec) SchemaNames() []string {
	names := make([]string, 0, len(s.Components.Schemas))
	for n := range s.Components.Schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func LoadSpec(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	return ParseSpec(data, path)
}

func ParseSpec(data []byte, path string) (*Spec, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: parse yaml: %w", path, err)
	}
	if err := checkExtensions(&root, path); err != nil {
		return nil, err
	}

	var spec Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("%s: parse spec: %w", path, err)
	}
	spec.Path = path
	return &spec, nil
}

// checkExtensions обходит документ и проверяет, что все ключи с префиксом x-
// известны. Неизвестное расширение — ошибка: иначе опечатка в
// x-babble-idempotent тихо превратит идемпотентный метод в неидемпотентный.
func checkExtensions(n *yaml.Node, path string) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if strings.HasPrefix(key.Value, "x-") && !knownExtensions[key.Value] {
				return fmt.Errorf("%s:%d: unknown extension %q", path, key.Line, key.Value)
			}
			if err := checkExtensions(n.Content[i+1], path); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range n.Content {
		if err := checkExtensions(c, path); err != nil {
			return err
		}
	}
	return nil
}

// RefName достаёт имя схемы из $ref.
func RefName(ref string) string {
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// Resolve разворачивает $ref в схему компонента.
func (s *Spec) Resolve(sch *Schema) (*Schema, string, error) {
	if sch == nil || sch.Ref == "" {
		return sch, "", nil
	}
	name := RefName(sch.Ref)
	target, ok := s.Components.Schemas[name]
	if !ok {
		return nil, name, fmt.Errorf("%s: unknown $ref %q", s.Path, sch.Ref)
	}
	return target, name, nil
}
