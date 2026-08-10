package codegen

import (
	"go/format"
	"strings"
	"testing"
)

// richSpec задействует все ветки рендера сразу: enum, массив, карту, дату,
// вложенную структуру, ограничения, default и x-babble-oneof.
const richSpec = `
openapi: "3.0.0"
info: {title: T, version: "1.0.0"}
x-babble-service: rich
paths:
  /doIt:
    post:
      operationId: doIt
      x-babble-idempotent: true
      x-babble-public: true
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/DoItIn'}
      responses:
        200: {description: OK, content: {application/json: {schema: {$ref: '#/components/schemas/DoItOut'}}}}
        400: {description: Bad, content: {application/json: {schema: {$ref: '#/components/schemas/Error'}}}}
        500: {description: Err, content: {application/json: {schema: {$ref: '#/components/schemas/Error'}}}}
components:
  schemas:
    Error:
      type: object
      required: [error]
      properties:
        error: {$ref: '#/components/schemas/ErrorInfo'}
    ErrorInfo:
      type: object
      required: [message]
      properties:
        kind: {type: string}
        message: {type: string}
    DoItIn:
      type: object
      required: [name, kind]
      properties:
        name: {type: string, minLength: 1, maxLength: 64, pattern: '^[a-z]+$'}
        kind: {type: string, enum: [FAST, SLOW]}
        weight: {type: number, minimum: 0.5, maximum: 10}
        count: {type: integer, format: int32, minimum: 1, default: 3}
        tags: {type: array, minItems: 1, maxItems: 5, items: {type: string, maxLength: 8}}
        nested: {$ref: '#/components/schemas/Nested'}
        byName: {type: object, additionalProperties: {$ref: '#/components/schemas/Nested'}}
        createdAt: {type: string, format: date-time}
        blob: {type: string, format: byte}
    Nested:
      type: object
      required: [id]
      properties:
        id: {type: string, minLength: 1}
        children: {type: array, items: {$ref: '#/components/schemas/Nested'}}
    DoItOut:
      type: object
      required: [ok]
      properties:
        ok: {type: boolean}
        error: {$ref: '#/components/schemas/DoItError'}
    DoItError:
      type: object
      x-babble-oneof: true
      properties:
        busy: {$ref: '#/components/schemas/Nested'}
        broken: {$ref: '#/components/schemas/Nested'}
`

func buildRich(t *testing.T) *Model {
	t.Helper()
	spec, err := ParseSpec([]byte(richSpec), "rich.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if errs := LintSpec(spec); len(errs) > 0 {
		t.Fatalf("lint: %v", errs)
	}
	m, err := BuildModel(spec)
	if err != nil {
		t.Fatalf("build model: %v", err)
	}
	return m
}

// Главная страховка рендера: любой сгенерированный файл обязан быть валидным
// Go. Кодоген, который пишет непарсящийся код, должен падать, а не молчать.
func TestGeneratedCodeParses(t *testing.T) {
	m := buildRich(t)

	files := map[string]string{
		"dto.go":        RenderDTO("dto", m, "rich.yaml", "0"),
		"const.go":      RenderConst("dto", m, "rich.yaml", "0"),
		"sdk.go":        RenderServerSDK(m, "example.com/x/internal/generated/service/dto", "rich.yaml", "0"),
		"client_sdk.go": RenderClientSDK("rich", m, "rich.yaml", "0"),
		"client_dto.go": RenderDTO("rich", m, "rich.yaml", "0"),
		"stub.go":       RenderStub(m.Methods[0], "example.com/x/internal/generated/service/dto"),
	}
	for name, src := range files {
		if _, err := format.Source([]byte(src)); err != nil {
			t.Fatalf("%s does not parse: %v\n%s", name, err, src)
		}
	}
}

func TestTypeMapping(t *testing.T) {
	m := buildRich(t)
	dto := RenderDTO("dto", m, "rich.yaml", "0")

	want := []string{
		// Указатель ⟺ поле не в required.
		"Name string `json:\"name\"`",
		"Count *int32 `json:\"count,omitempty\"`",
		// format
		"CreatedAt *time.Time `json:\"createdAt,omitempty\"`",
		"Blob []byte `json:\"blob,omitempty\"`",
		// enum — именованный строковый тип, а не строка
		"Kind DoItInKind `json:\"kind\"`",
		// массивы и карты
		"Tags []string `json:\"tags,omitempty\"`",
		"ByName map[string]Nested `json:\"byName,omitempty\"`",
		// конструктор варианта суммы
		"func NewDoItErrorBusy(v Nested) *DoItError",
	}
	normalized := normalizeSpaces(dto)
	for _, w := range want {
		if !strings.Contains(normalized, normalizeSpaces(w)) {
			t.Errorf("generated dto lacks %q", w)
		}
	}
}

func TestValidationIsGenerated(t *testing.T) {
	m := buildRich(t)
	dto := normalizeSpaces(RenderDTO("dto", m, "rich.yaml", "0"))

	want := []string{
		"babble.CheckRequired(babble.Field(path, \"name\")",
		"babble.CheckMinLength(babble.Field(path, \"name\"), string(d.Name), 1)",
		"babble.CheckPattern(babble.Field(path, \"name\"), string(d.Name), \"^[a-z]+$\")",
		"babble.CheckEnum(babble.Field(path, \"kind\"), d.Kind, DoItInKind_All)",
		"babble.CheckMinimum(babble.Field(path, \"weight\"), (*d.Weight), float64(0.5))",
		"babble.CheckMinItems(babble.Field(path, \"tags\"), len(d.Tags), 1)",
		// рекурсия в элементы массива и значения карты
		"babble.Index(babble.Field(path, \"tags\"), i_)",
		"babble.Key(babble.Field(path, \"byName\"), k_)",
		// вложенная структура валидируется своим методом
		"d.Nested.Validate_(v, babble.Field(path, \"nested\"))",
		// инвариант суммы
		"babble.CheckOneOf(path, set_)",
	}
	for _, w := range want {
		if !strings.Contains(dto, normalizeSpaces(w)) {
			t.Errorf("generated dto lacks validation %q", w)
		}
	}
}

func TestEnumRendering(t *testing.T) {
	m := buildRich(t)
	consts := normalizeSpaces(RenderConst("dto", m, "rich.yaml", "0"))

	for _, w := range []string{
		"type DoItInKind string",
		"DoItInKindFast DoItInKind = \"FAST\"",
		"DoItInKindSlow DoItInKind = \"SLOW\"",
		"var DoItInKind_All = []DoItInKind{",
		"func (v DoItInKind) Valid() bool",
	} {
		if !strings.Contains(consts, normalizeSpaces(w)) {
			t.Errorf("generated const lacks %q", w)
		}
	}
}

// Флаги из контракта обязаны доехать до сгенерированного кода: на них завязаны
// ретраи и авторизация.
func TestContractFlagsReachGeneratedCode(t *testing.T) {
	m := buildRich(t)

	sdk := normalizeSpaces(RenderServerSDK(m, "example.com/x/dto", "rich.yaml", "0"))
	if !strings.Contains(sdk, normalizeSpaces("Idempotent: true")) || !strings.Contains(sdk, normalizeSpaces("Public: true")) {
		t.Fatalf("server sdk lost contract flags:\n%s", sdk)
	}

	client := normalizeSpaces(RenderClientSDK("rich", m, "rich.yaml", "0"))
	if !strings.Contains(client, normalizeSpaces("babble.CallOpts{Idempotent: true}")) {
		t.Fatalf("client sdk lost the idempotent flag:\n%s", client)
	}
}

func TestDefaultIsFilled(t *testing.T) {
	m := buildRich(t)
	dto := normalizeSpaces(RenderDTO("dto", m, "rich.yaml", "0"))
	if !strings.Contains(dto, normalizeSpaces("dv := int32(3)")) {
		t.Fatalf("default value for count was not generated")
	}
}

func normalizeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
