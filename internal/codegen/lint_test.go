package codegen

import (
	"strings"
	"testing"
)

// validSpec — минимальный контракт, проходящий профиль; тесты портят его по
// одному месту за раз.
const validSpec = `
openapi: "3.0.0"
info: {title: T, version: "1.0.0"}
x-babble-service: users
paths:
  /create:
    post:
      operationId: create
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/CreateIn'}
      responses:
        200: {description: OK, content: {application/json: {schema: {$ref: '#/components/schemas/CreateOut'}}}}
        400: {description: Bad, content: {application/json: {schema: {$ref: '#/components/schemas/JSendError'}}}}
        500: {description: Err, content: {application/json: {schema: {$ref: '#/components/schemas/JSendError'}}}}
components:
  schemas:
    JSendError:
      type: object
      required: [status, message]
      properties:
        status: {type: string}
        message: {type: string}
    CreateIn:
      type: object
      required: [phone]
      properties:
        phone: {type: string}
    CreateOut:
      type: object
      required: [ok]
      properties:
        ok: {type: boolean}
`

func lintYAML(t *testing.T, src string) []LintError {
	t.Helper()
	spec, err := ParseSpec([]byte(src), "test.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return LintSpec(spec)
}

func TestValidSpecPasses(t *testing.T) {
	if errs := lintYAML(t, validSpec); len(errs) > 0 {
		t.Fatalf("valid spec rejected: %v", errs)
	}
}

func TestProfileViolations(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
		want string
	}{
		{
			name: "get is forbidden",
			edit: func(s string) string {
				return strings.Replace(s, "    post:\n", "    get:\n      operationId: create\n    post:\n", 1)
			},
			want: "only post is allowed",
		},
		{
			name: "path must equal operationId",
			edit: func(s string) string { return strings.Replace(s, "  /create:", "  /users/create:", 1) },
			want: "path must be",
		},
		{
			name: "operationId must be lowerCamel",
			edit: func(s string) string {
				return strings.Replace(s, "operationId: create", "operationId: Create", 1)
			},
			want: "lowerCamelCase",
		},
		{
			name: "parameters are forbidden",
			edit: func(s string) string {
				return strings.Replace(s, "      requestBody:",
					"      parameters:\n        - {name: id, in: query, schema: {type: string}}\n      requestBody:", 1)
			},
			want: "parameters are not allowed",
		},
		{
			name: "extra status code",
			edit: func(s string) string {
				return strings.Replace(s, "        500:",
					"        409: {description: Conflict, content: {application/json: {schema: {$ref: '#/components/schemas/JSendError'}}}}\n        500:", 1)
			},
			want: "is not allowed (only 200, 400, 500)",
		},
		{
			name: "inline request schema",
			edit: func(s string) string {
				return strings.Replace(s, "            schema: {$ref: '#/components/schemas/CreateIn'}",
					"            schema: {type: object, properties: {phone: {type: string}}}", 1)
			},
			want: "inline schemas are not allowed",
		},
		{
			name: "wrong schema name",
			edit: func(s string) string {
				return strings.Replace(s, "$ref: '#/components/schemas/CreateIn'", "$ref: '#/components/schemas/CreateOut'", 1)
			},
			want: "schema must be CreateIn",
		},
		{
			name: "service name is required",
			edit: func(s string) string { return strings.Replace(s, "x-babble-service: users\n", "", 1) },
			want: "x-babble-service is required",
		},
		{
			name: "enum values are UPPER_SNAKE",
			edit: func(s string) string {
				return strings.Replace(s, "        phone: {type: string}", "        phone: {type: string, enum: [mobile]}", 1)
			},
			want: "must be UPPER_SNAKE_CASE",
		},
		{
			name: "property must be lowerCamel",
			edit: func(s string) string {
				return strings.Replace(s, "        ok: {type: boolean}", "        Ok: {type: boolean}", 1)
			},
			want: "must be lowerCamelCase",
		},
		{
			name: "empty schema",
			edit: func(s string) string {
				return s + "    Empty:\n      type: object\n      properties: {}\n"
			},
			want: "empty messages are not allowed",
		},
		{
			name: "oneof variants must be optional",
			edit: func(s string) string {
				return strings.Replace(s, "    CreateOut:\n      type: object\n      required: [ok]",
					"    CreateOut:\n      type: object\n      x-babble-oneof: true\n      required: [ok]", 1)
			},
			want: "variants must all be optional",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := lintYAML(t, tc.edit(validSpec))
			for _, e := range errs {
				if strings.Contains(e.Msg, tc.want) {
					return
				}
			}
			t.Fatalf("want error containing %q, got %v", tc.want, errs)
		})
	}
}

// Опечатка в расширении обязана падать: молча проигнорированный
// x-babble-idempotant превратил бы идемпотентный метод в обычный.
func TestUnknownExtensionIsRejected(t *testing.T) {
	src := strings.Replace(validSpec, "      operationId: create", "      operationId: create\n      x-babble-idempotant: true", 1)
	if _, err := ParseSpec([]byte(src), "test.yaml"); err == nil {
		t.Fatal("want error on unknown extension")
	}
}

func TestRequiredCycleIsRejected(t *testing.T) {
	src := validSpec + `    Node:
      type: object
      required: [child]
      properties:
        child: {$ref: '#/components/schemas/Node'}
`
	errs := lintYAML(t, src)
	for _, e := range errs {
		if strings.Contains(e.Msg, "cycle") {
			return
		}
	}
	t.Fatalf("want a cycle error, got %v", errs)
}

// Необязательное поле — указатель, цикл через него разрывается.
func TestOptionalCycleIsAllowed(t *testing.T) {
	src := validSpec + `    Node:
      type: object
      required: [id]
      properties:
        id: {type: string}
        child: {$ref: '#/components/schemas/Node'}
`
	for _, e := range lintYAML(t, src) {
		if strings.Contains(e.Msg, "cycle") {
			t.Fatalf("optional recursion rejected: %v", e)
		}
	}
}
