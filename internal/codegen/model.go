package codegen

import (
	"fmt"
	"sort"
	"strings"
)

// Модель — то, что кодоген строит из контракта перед рендерингом. Здесь
// принимаются все решения о типах и об опциональности; шаблоны их только
// печатают.

type Kind int

const (
	KindString Kind = iota
	KindInt64
	KindInt32
	KindFloat64
	KindFloat32
	KindBool
	KindTime
	KindBytes
	KindEnum
	KindStruct
	KindArray
	KindMap
	KindAny
)

type Constraints struct {
	MinLength *int
	MaxLength *int
	Pattern   string
	Minimum   *float64
	Maximum   *float64
	MinItems  *int
	MaxItems  *int
}

func (c Constraints) empty() bool {
	return c.MinLength == nil && c.MaxLength == nil && c.Pattern == "" &&
		c.Minimum == nil && c.Maximum == nil && c.MinItems == nil && c.MaxItems == nil
}

type TypeRef struct {
	Kind Kind
	// GoType — тип без ведущей звёздочки; опциональность живёт в Field.Ptr.
	GoType     string
	EnumName   string
	StructName string
	Elem       *TypeRef
	C          Constraints
}

// NeedsValidation отвечает, нужно ли для этого типа вообще генерировать код
// проверки: иначе Validate_ забивается пустыми блоками.
func (t *TypeRef) NeedsValidation() bool {
	if t == nil {
		return false
	}
	if !t.C.empty() {
		return true
	}
	switch t.Kind {
	case KindEnum, KindStruct:
		return true
	case KindArray, KindMap:
		return t.Elem.NeedsValidation()
	default:
		return false
	}
}

type Field struct {
	GoName   string
	JSONTag  string
	Doc      string
	Required bool
	Type     *TypeRef
	Default  any
}

// Optional: поле не в required (nullable — синоним required: false).
func (f *Field) Optional() bool { return !f.Required }

// Nilable: тип и так различает «пусто» и «не задано», лишняя звёздочка не
// нужна — *[]string читается плохо и ничего не добавляет.
func (f *Field) Nilable() bool {
	switch f.Type.Kind {
	case KindArray, KindMap, KindBytes:
		return true
	default:
		return false
	}
}

// Ptr: указатель тогда и только тогда, когда поле необязательное и его тип не
// nil-абелен сам по себе.
func (f *Field) Ptr() bool { return f.Optional() && !f.Nilable() }

func (f *Field) GoDecl() string {
	if f.Ptr() {
		return "*" + f.Type.GoType
	}
	return f.Type.GoType
}

func (f *Field) Tag() string {
	tag := f.JSONTag
	if f.Optional() {
		tag += ",omitempty"
	}
	return fmt.Sprintf("`json:%q`", tag)
}

type EnumValue struct {
	ConstName string
	Value     string
}

type Enum struct {
	Name   string
	Values []EnumValue
}

type Struct struct {
	Name   string
	Doc    string
	OneOf  bool
	Fields []*Field
}

type MethodModel struct {
	Name       string // operationId, он же маршрут
	GoName     string // Pascal
	Pkg        string // snake, имя пакета стаба
	In         string // имя структуры
	Out        string
	Doc        string
	Idempotent bool
	Public     bool
}

type Model struct {
	Service string
	Structs []*Struct
	Enums   []*Enum
	Methods []*MethodModel

	// UsesTime — хоть одно поле имеет тип time.Time.
	UsesTime bool
}

func BuildModel(s *Spec) (*Model, error) {
	m := &Model{Service: s.Service}

	for _, name := range s.SchemaNames() {
		schema := s.Components.Schemas[name]
		st := &Struct{Name: name, Doc: schema.Description, OneOf: schema.OneOf}

		required := map[string]bool{}
		for _, r := range schema.Required {
			required[r] = true
		}

		props := make([]string, 0, len(schema.Properties))
		for p := range schema.Properties {
			props = append(props, p)
		}
		sort.Strings(props)

		for _, prop := range props {
			p := schema.Properties[prop]
			ref, err := m.typeRef(s, name, prop, p)
			if err != nil {
				return nil, err
			}
			st.Fields = append(st.Fields, &Field{
				GoName:   ToPascal(prop),
				JSONTag:  prop,
				Doc:      p.Description,
				Required: required[prop],
				Type:     ref,
				Default:  p.Default,
			})
		}
		m.Structs = append(m.Structs, st)
	}

	sort.Slice(m.Enums, func(i, j int) bool { return m.Enums[i].Name < m.Enums[j].Name })

	for _, method := range s.Methods() {
		op := method.Operation
		goName := ToPascal(op.OperationID)
		m.Methods = append(m.Methods, &MethodModel{
			Name:       op.OperationID,
			GoName:     goName,
			Pkg:        SafeIdent(ToSnake(op.OperationID)),
			In:         goName + "In",
			Out:        goName + "Out",
			Doc:        firstNonEmpty(op.Summary, op.Description),
			Idempotent: op.Idempotent,
			Public:     op.Public,
		})
	}
	return m, nil
}

func (m *Model) typeRef(s *Spec, schemaName, propName string, p *Schema) (*TypeRef, error) {
	if p == nil {
		return &TypeRef{Kind: KindAny, GoType: "any"}, nil
	}
	c := Constraints{
		MinLength: p.MinLength,
		MaxLength: p.MaxLength,
		Pattern:   p.Pattern,
		Minimum:   p.Minimum,
		Maximum:   p.Maximum,
		MinItems:  p.MinItems,
		MaxItems:  p.MaxItems,
	}

	if p.Ref != "" {
		name := RefName(p.Ref)
		if _, ok := s.Components.Schemas[name]; !ok {
			return nil, fmt.Errorf("%s: schema %s: property %q references unknown schema %q", s.Path, schemaName, propName, p.Ref)
		}
		return &TypeRef{Kind: KindStruct, GoType: name, StructName: name, C: c}, nil
	}

	if len(p.Enum) > 0 {
		enumName := schemaName + ToPascal(propName)
		m.addEnum(enumName, p.Enum)
		return &TypeRef{Kind: KindEnum, GoType: enumName, EnumName: enumName, C: c}, nil
	}

	switch p.Type {
	case "string":
		switch p.Format {
		case "date-time":
			m.UsesTime = true
			return &TypeRef{Kind: KindTime, GoType: "time.Time", C: c}, nil
		case "byte", "binary":
			return &TypeRef{Kind: KindBytes, GoType: "[]byte", C: c}, nil
		default:
			return &TypeRef{Kind: KindString, GoType: "string", C: c}, nil
		}
	case "integer":
		if p.Format == "int32" {
			return &TypeRef{Kind: KindInt32, GoType: "int32", C: c}, nil
		}
		return &TypeRef{Kind: KindInt64, GoType: "int64", C: c}, nil
	case "number":
		if p.Format == "float" || p.Format == "float32" {
			return &TypeRef{Kind: KindFloat32, GoType: "float32", C: c}, nil
		}
		return &TypeRef{Kind: KindFloat64, GoType: "float64", C: c}, nil
	case "boolean":
		return &TypeRef{Kind: KindBool, GoType: "bool", C: c}, nil
	case "array":
		elem, err := m.typeRef(s, schemaName, propName+"[]", p.Items)
		if err != nil {
			return nil, err
		}
		return &TypeRef{Kind: KindArray, GoType: "[]" + elem.GoType, Elem: elem, C: c}, nil
	case "object":
		if p.AdditionalProperties != nil {
			elem, err := m.typeRef(s, schemaName, propName+"[*]", p.AdditionalProperties)
			if err != nil {
				return nil, err
			}
			return &TypeRef{Kind: KindMap, GoType: "map[string]" + elem.GoType, Elem: elem, C: c}, nil
		}
		return nil, fmt.Errorf("%s: schema %s: property %q: inline objects are not allowed", s.Path, schemaName, propName)
	case "":
		return &TypeRef{Kind: KindAny, GoType: "any", C: c}, nil
	default:
		return nil, fmt.Errorf("%s: schema %s: property %q: unsupported type %q", s.Path, schemaName, propName, p.Type)
	}
}

func (m *Model) addEnum(name string, values []string) {
	for _, e := range m.Enums {
		if e.Name == name {
			return
		}
	}
	enum := &Enum{Name: name}
	for _, v := range values {
		enum.Values = append(enum.Values, EnumValue{
			ConstName: name + ToPascal(strings.ToLower(v)),
			Value:     v,
		})
	}
	m.Enums = append(m.Enums, enum)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
