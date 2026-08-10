package codegen

import "strings"

// RenderServerSDK печатает плоский service/sdk.go: по одному описанию метода
// плюс дескриптор сервиса.
func RenderServerSDK(m *Model, dtoImport, source, sum string) string {
	var b buf
	b.p("%s", header(source, sum))
	b.p("package service")
	b.p("")
	b.p("import (")
	b.p("\t\"github.com/mosdev-tech/babble\"")
	b.p("")
	b.p("\t\"%s\"", dtoImport)
	b.p(")")
	b.p("")
	b.p("// ServiceName — имя сервиса из x-babble-service.")
	b.p("const ServiceName = %q", m.Service)
	b.p("")

	for _, method := range m.Methods {
		if method.Doc != "" {
			b.p("// %s %s", method.GoName, method.Doc)
		}
		b.p("var %s = babble.Method[dto.%s, dto.%s]{", method.GoName, method.In, method.Out)
		b.p("\tName:       %q,", method.Name)
		b.p("\tIdempotent: %t,", method.Idempotent)
		b.p("\tPublic:     %t,", method.Public)
		b.p("}")
		b.p("")
	}

	b.p("// Methods перечисляет все методы контракта — для реестров и проверок")
	b.p("// конфигурации.")
	b.p("var Methods = []babble.MethodDescriptor{")
	for _, method := range m.Methods {
		b.p("\t%s.Descriptor(),", method.GoName)
	}
	b.p("}")
	return b.String()
}

// RenderClientSDK печатает клиента: интерфейс, реализацию и конструкторы.
func RenderClientSDK(pkg string, m *Model, source, sum string) string {
	var b buf
	b.p("%s", header(source, sum))
	b.p("package %s", pkg)
	b.p("")
	b.p("import (")
	b.p("\t\"context\"")
	b.p("")
	b.p("\t\"github.com/mosdev-tech/babble\"")
	b.p(")")
	b.p("")
	b.p("// ServiceName — имя сервиса; адрес резолвится из SERVICE_%s_URL.", envSuffix(m.Service))
	b.p("const ServiceName = %q", m.Service)
	b.p("")
	b.p("// Service нужен, чтобы babble.ClientFor[%s.Service] знал имя сервиса.", pkg)
	b.p("type Service struct{}")
	b.p("")
	b.p("func (Service) ServiceName__() string { return ServiceName }")
	b.p("")

	b.p("type Client interface {")
	for _, method := range m.Methods {
		if method.Doc != "" {
			b.p("\t// %s", method.Doc)
		}
		b.p("\t%s(ctx context.Context, in *%s) (*%s, error)", method.GoName, method.In, method.Out)
	}
	b.p("}")
	b.p("")
	b.p("type client struct {")
	b.p("\trpc babble.Client")
	b.p("}")
	b.p("")
	b.p("func New(rpc babble.Client) Client { return &client{rpc: rpc} }")
	b.p("")
	b.p("// NewFromEnv собирает клиента, взяв адрес из SERVICE_%s_URL.", envSuffix(m.Service))
	b.p("func NewFromEnv(opts ...babble.ClientOption) (Client, error) {")
	b.p("\trpc, err := babble.NewClient(append([]babble.ClientOption{babble.WithBaseURLFromEnv(ServiceName)}, opts...)...)")
	b.p("\tif err != nil {")
	b.p("\t\treturn nil, err")
	b.p("\t}")
	b.p("\treturn New(rpc), nil")
	b.p("}")
	b.p("")

	for _, method := range m.Methods {
		b.p("func (c *client) %s(ctx context.Context, in *%s) (*%s, error) {", method.GoName, method.In, method.Out)
		b.p("\treturn babble.Call[%s, %s](ctx, c.rpc, %q, in, babble.CallOpts{Idempotent: %t})",
			method.In, method.Out, method.Name, method.Idempotent)
		b.p("}")
		b.p("")
	}
	return b.String()
}

// RenderStub — заготовка бизнес-хендлера. Пишется один раз и больше никогда не
// перезаписывается, поэтому шапки DO NOT EDIT здесь нет.
func RenderStub(method *MethodModel, dtoImport string) string {
	var b buf
	b.p("package %s", method.Pkg)
	b.p("")
	b.p("import (")
	b.p("\t\"context\"")
	b.p("\t\"errors\"")
	b.p("")
	b.p("\t\"%s\"", dtoImport)
	b.p(")")
	b.p("")
	if method.Doc != "" {
		b.p("// Handler %s", method.Doc)
	}
	b.p("type Handler struct{}")
	b.p("")
	b.p("func New() *Handler { return &Handler{} }")
	b.p("")
	b.p("func (h *Handler) Handle(ctx context.Context, in *dto.%s) (*dto.%s, error) {", method.In, method.Out)
	b.p("\t// TODO: implement")
	b.p("\treturn nil, errors.New(%q)", "not implemented: "+method.Name)
	b.p("}")
	return b.String()
}

// envSuffix — часть имени переменной окружения с адресом сервиса
// (users → USERS), нужна только для комментариев в сгенерированном коде.
func envSuffix(service string) string {
	return strings.ToUpper(strings.ReplaceAll(service, "-", "_"))
}
