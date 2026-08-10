# babble

Рантайм и кодогенератор для HTTP-сервисов: OpenAPI 3.0 (профиль `rpc`) как
единственный IDL — и для внешнего периметра, и для межсервисных вызовов.
Транспорт — JSON поверх HTTP, один маршрут на метод: `POST /<operationId>`.

Две части в одном модуле `github.com/mosdev-tech/babble`:

- **рантайм** (корень: `server.go`, `client.go`, `errors.go`, `validate.go`,
  `metadata.go`) — ноль внешних зависимостей;
- **кодогенератор** `cmd/babble` — YAML → серверный SDK, стабы хендлеров,
  типизированные клиенты. Единственная зависимость — `gopkg.in/yaml.v3`.

Полное описание решений и их обоснование — в [SPEC.md](SPEC.md).

## Быстрый старт

1. В корне сервиса создать `babble/service.yaml` — контракт в профиле `rpc`:

```yaml
openapi: "3.0.0"
x-babble-service: users
info: { title: Users API, version: "1.0.0" }

paths:
  /getById:
    post:
      operationId: getById
      x-babble-idempotent: true
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/GetByIdIn' }
      responses:
        200: { description: OK,            content: { application/json: { schema: { $ref: '#/components/schemas/GetByIdOut' } } } }
        400: { description: Bad request,   content: { application/json: { schema: { $ref: '#/components/schemas/JSendError' } } } }
        500: { description: Internal Error, content: { application/json: { schema: { $ref: '#/components/schemas/JSendError' } } } }

components:
  schemas:
    GetByIdIn:
      type: object
      required: [ id ]
      properties:
        id: { type: integer, format: int64 }
    GetByIdOut:
      type: object
      required: [ found ]
      properties:
        found: { type: boolean }
        user:  { $ref: '#/components/schemas/UserEntity' }
    UserEntity:
      type: object
      required: [ id, phone ]
      properties:
        id:    { type: integer, format: int64 }
        phone: { type: string }
    JSendError:
      type: object
      description: "Конверт транспортной ошибки (400/500)"
      required: [ status, message ]
      properties:
        status:  { type: string }
        message: { type: string }
```

2. Сгенерировать код: `go run github.com/mosdev-tech/babble/cmd/babble gen`.
   Появятся `internal/generated/**` и стаб `internal/api/get_by_id/handler.go`.

3. Написать тело хендлера — сигнатура задана контрактом:

```go
func (h *Handler) Handle(ctx context.Context, in *dto.GetByIdIn) (*dto.GetByIdOut, error) {
    user, ok := h.store.ByID(in.Id)
    if !ok {
        // «Не нашли» — обычный исход, а не ошибка транспорта.
        return &dto.GetByIdOut{Found: false}, nil
    }
    return &dto.GetByIdOut{Found: true, User: &user}, nil
}
```

4. Собрать сервер из методов сгенерированного SDK — см. раздел [Сервер](#сервер).

Рабочий сквозной пример (контракт, зависимость, интеграционные тесты) —
в `example/users` и `example/contacts`.

## Раскладка сервиса

```
babble/
  service.yaml               # свой контракт
  clients/contacts.yaml      # контракт зависимости в нужном объёме;
                             # файл рукописный
internal/
  generated/                 # сносится и перегенерируется целиком; коммитится
    service/sdk.go
    service/dto/{dto.go,const.go}
    clients/<name>/{sdk.go,dto.go,const.go}
    clients/<self>/           # self-client — для интеграционных тестов
  api/<method>/handler.go    # стаб; пишется один раз, кодоген его не трогает
```

## Команды

```sh
go run github.com/mosdev-tech/babble/cmd/babble gen    # генерация
go run github.com/mosdev-tech/babble/cmd/babble lint   # правила профиля
go run github.com/mosdev-tech/babble/cmd/babble help   # список команд
```

Обе команды запускаются из корня сервиса — того каталога, где лежат `go.mod` и
`babble/`.

Через Taskfile: `task codegen`, `task lint`, `task test`, `task run`.

Конфига у кодогена нет: пути — соглашение, Go-модуль берётся из `go.mod`.

## Контракты зависимостей

`babble/clients/<name>.yaml` пишется руками — это контракт зависимости в том
объёме, который нужен сервису. Имя файла обязано совпадать с
`x-babble-service` внутри, и файл проходит те же правила профиля, что и
собственный контракт.

Кодоген его только читает. С оригиналом файл никак не сверяется, поэтому
`x-babble-idempotent`, типы полей и состав методов приходится держать в
соответствии вручную; расхождение проявится в рантайме, а не на сборке.

## Профиль контракта `rpc`

OpenAPI используется как синтаксис, но допускается только RPC-подмножество —
оно проверяется линтом, и без успешного линта генерация не запускается:

- только `post`, путь ровно `/{operationId}`, `operationId` — lowerCamelCase;
- `requestBody` обязателен, только `application/json`, только `$ref` на
  именованную схему `<Method>In`; ответ 200 — `<Method>Out`;
- ответы — ровно `200`, `400`, `500`; других кодов нет;
- `parameters` запрещены: ни path-, ни query-параметров;
- схемы — UpperCamelCase, поля — lowerCamelCase, значения enum — UPPER_SNAKE;
- inline-схемы, пустые схемы и циклы по обязательным полям запрещены;
- неизвестное расширение `x-*` — ошибка, а не молчаливое игнорирование.

Расширения: `x-babble-service` (имя сервиса, в корне),
`x-babble-idempotent`, `x-babble-public` (на операции), `x-babble-oneof`
(на схеме).

## Модель ошибок

Транспорт знает ровно три исхода:

| Исход | HTTP | Go на клиенте |
|---|---|---|
| успех | 200 | `(*Out, nil)` |
| клиент нарушил контракт | 400 | `*babble.ValidationError` |
| сервер не смог | 500 | `*babble.ServerError` |

Что угодно ещё — `*babble.TransportError`.

**Бизнес-ошибки в транспорт не выносятся** — они живут в DTO ответа как сумма
вариантов (`x-babble-oneof`), у которой кодоген генерирует конструкторы и
проверку «не более одного непустого поля»:

```yaml
CreateOut:
  required: [ ok ]
  properties:
    ok:    { type: boolean }
    user:  { $ref: '#/components/schemas/UserEntity' }
    error: { $ref: '#/components/schemas/CreateError' }

CreateError:
  x-babble-oneof: true
  properties:
    phoneTaken: { $ref: '#/components/schemas/PhoneTakenError' }
```

```go
return &dto.CreateOut{
    Ok:    false,
    Error: dto.NewCreateErrorPhoneTaken(dto.PhoneTakenError{ExistingUserId: id}),
}, nil
```

## Сервер

```go
srv, err := babble.NewServer(
    babble.WithSettings(babble.Settings{Address: ":8080", ShutdownTimeout: 10 * time.Second}),
    babble.WithMethod(service.Create, create.New(store, contactsClient).Handle),
    babble.WithMethod(service.GetById, getbyid.New(store).Handle),
    babble.WithServerInterceptor(requireAuth),
    babble.WithHealthCheck(func(ctx context.Context) error { return db.PingContext(ctx) }),
)
if err != nil {
    return err
}
return srv.Run(ctx) // graceful shutdown по отмене ctx
```

Бизнес-хендлер: `func(ctx context.Context, in *dto.CreateIn) (*dto.CreateOut, error)`.
Типы `In`/`Out` в `WithMethod` выводятся из хендлера — несовпадение с
`service.Create` не компилируется.

Расширение — два уровня: `WithHTTPMiddleware` (до маршрутизации: CORS,
request-id, аутентификация) и `WithServerInterceptor` (после декода: метрики,
логирование, авторизация — дескриптор несёт `Public` из контракта).

Заголовки входящего запроса — `babble.Metadata(ctx)`, ответные —
`babble.SetResponseHeader(ctx, ...)`.

`GET /health` монтируется всегда: `200 {"status":"serving"}`, а с
`WithHealthCheck` — `503 {"status":"not_serving"}` при ошибке.
`srv.Handler()` отдаёт `http.Handler` для тестов через `httptest`.

## Клиент

Адрес — по конвенции из `SERVICE_<UPPER(name)>_URL`:

```go
opt := babble.WithRecommendedClientSettings(babble.RecommendedClientSettings{
    Timeout:        300 * time.Millisecond,
    Interceptors:   []babble.ClientInterceptor{logCalls},
    ForwardHeaders: []string{"X-Request-Id"},
})

rpc, err := babble.ClientFor[contacts.Service](opt)   // общие настройки
client := contacts.New(rpc)

out, err := client.Sync(ctx, &contacts.SyncIn{UserId: 7, Phone: "+7999..."})
```

Ещё два способа: `contacts.NewFromEnv(opt)` и явный
`babble.NewClient(babble.WithBaseURL(ts.URL))` для тестов.

Ретраи — только для методов с `x-babble-idempotent: true`, только на
транспортных сбоях и `429`/`503`. На `500` — никогда: сервер мог начать
выполнять и упасть. Таймауты задаются в опциях (`WithTimeout`,
`WithMethodTimeout`), а не в контракте — это бюджет потребителя.

## Локальная разработка

Примеры-сервисы (`example/users`, `example/contacts`) — отдельные Go-модули,
которые видят либу через `go.work` в корне репозитория. Он должен лежать именно
в корне: GoLand и gopls применяют только `go.work` внутри корня проекта.

```sh
go test ./...                        # рантайм и кодоген
cd example/users && task codegen && task test
```

Go 1.26.5.
