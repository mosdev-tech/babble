// Интеграционные тесты бьют по собственному серверу сгенерированным
// self-client'ом через httptest — без сети и без ручной сборки запросов.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mosdev-tech/babble"
	"github.com/mosdev-tech/babble/users/internal/api/handler/create"
	get_by_id "github.com/mosdev-tech/babble/users/internal/api/handler/get_by_id"
	"github.com/mosdev-tech/babble/users/internal/generated/clients/contacts"
	"github.com/mosdev-tech/babble/users/internal/generated/clients/users"
	"github.com/mosdev-tech/babble/users/internal/generated/service"
	"github.com/mosdev-tech/babble/users/internal/store"
)

type fakeContacts struct {
	calls int
	err   error
}

func (f *fakeContacts) Sync(_ context.Context, in *contacts.SyncIn) (*contacts.SyncOut, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &contacts.SyncOut{ContactId: "contact-1"}, nil
}

func newServer(t *testing.T, cs create.Contacts, opts ...babble.ServerOption) (*httptest.Server, users.Client) {
	t.Helper()

	st := store.New()
	all := append([]babble.ServerOption{
		babble.WithMethod(service.Create, create.New(st, cs).Handle),
		babble.WithMethod(service.GetById, get_by_id.New(st).Handle),
	}, opts...)

	srv, err := babble.NewServer(all...)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	rpc, err := babble.NewClient(babble.WithBaseURL(ts.URL))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return ts, users.New(rpc)
}

func TestCreateAndGet(t *testing.T) {
	cs := &fakeContacts{}
	_, client := newServer(t, cs)
	ctx := context.Background()

	out, err := client.Create(ctx, &users.CreateIn{Phone: "+79990000001", Role: users.CreateInRoleStudent})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !out.Ok || out.User == nil {
		t.Fatalf("create: unexpected out %+v", out)
	}
	if cs.calls != 1 {
		t.Fatalf("contacts.Sync called %d times, want 1", cs.calls)
	}

	got, err := client.GetById(ctx, &users.GetByIdIn{Id: out.User.Id})
	if err != nil {
		t.Fatalf("getById: %v", err)
	}
	if !got.Found || got.User.Phone != "+79990000001" {
		t.Fatalf("getById: unexpected out %+v", got)
	}
	if got.User.CreatedAt.IsZero() {
		t.Fatal("getById: createdAt was not decoded as time.Time")
	}
}

func TestGetByIdNotFound(t *testing.T) {
	_, client := newServer(t, &fakeContacts{})

	out, err := client.GetById(context.Background(), &users.GetByIdIn{Id: 999})
	if err != nil {
		t.Fatalf("getById: %v", err)
	}
	if out.Found {
		t.Fatal("getById: want found=false")
	}
}

// Бизнес-ошибка — поле в Out, а не код HTTP: err остаётся nil.
func TestBusinessErrorIsNotTransportError(t *testing.T) {
	_, client := newServer(t, &fakeContacts{})
	ctx := context.Background()

	in := &users.CreateIn{Phone: "+79990000002", Role: users.CreateInRoleStudent}
	if _, err := client.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}

	out, err := client.Create(ctx, in)
	if err != nil {
		t.Fatalf("create duplicate: unexpected transport error: %v", err)
	}
	if out.Ok {
		t.Fatal("create duplicate: want ok=false")
	}
	if out.Error == nil || out.Error.PhoneTaken == nil {
		t.Fatalf("create duplicate: want phoneTaken, got %+v", out.Error)
	}
	if out.Error.RoleNotAllowed != nil {
		t.Fatal("create duplicate: two variants set at once")
	}
}

func TestRoleNotAllowed(t *testing.T) {
	_, client := newServer(t, &fakeContacts{})

	out, err := client.Create(context.Background(), &users.CreateIn{
		Phone: "+79990000003",
		Role:  users.CreateInRoleAdmin,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.Error == nil || out.Error.RoleNotAllowed == nil {
		t.Fatalf("want roleNotAllowed, got %+v", out)
	}
}

// Клиент валидирует локально: невалидный телефон не доходит до сети.
func TestClientSideValidation(t *testing.T) {
	cs := &fakeContacts{}
	_, client := newServer(t, cs)

	_, err := client.Create(context.Background(), &users.CreateIn{Phone: "8-800", Role: users.CreateInRoleStudent})
	var ve *babble.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if ve.Field != "phone" {
		t.Fatalf("want field path %q, got %q", "phone", ve.Field)
	}
	if cs.calls != 0 {
		t.Fatal("request reached the server despite local validation")
	}
}

// Сервер отличает отсутствие обязательного поля от нулевого значения.
func TestServerSideRequired(t *testing.T) {
	ts, _ := newServer(t, &fakeContacts{})

	resp := post(t, ts.URL+"/create", `{"phone":"+79990000004"}`)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", resp.code, resp.body)
	}
	if !strings.Contains(resp.body, "role") {
		t.Fatalf("400 must name the missing field, got %s", resp.body)
	}
}

func TestServerSideEnum(t *testing.T) {
	ts, _ := newServer(t, &fakeContacts{})

	resp := post(t, ts.URL+"/create", `{"phone":"+79990000005","role":"WIZARD"}`)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", resp.code, resp.body)
	}
}

func TestUnknownMethodAndHealth(t *testing.T) {
	ts, _ := newServer(t, &fakeContacts{})

	resp := post(t, ts.URL+"/nope", `{}`)
	if resp.code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.code)
	}
	var body struct {
		Error struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp.body), &body); err != nil {
		t.Fatalf("404 must be JSON, got %s", resp.body)
	}
	if body.Error.Kind != "not_found" || body.Error.Message == "" {
		t.Fatalf("404 must carry the error envelope, got %s", resp.body)
	}

	health, err := http.Get(ts.URL + babble.HealthPath)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health: want 200, got %d", health.StatusCode)
	}
}

// Ошибка зависимости — ServerError: наружу 500 без деталей.
func TestDependencyFailureIsServerError(t *testing.T) {
	cs := &fakeContacts{err: errors.New("contacts is down")}
	_, client := newServer(t, cs)

	_, err := client.Create(context.Background(), &users.CreateIn{
		Phone: "+79990000006",
		Role:  users.CreateInRoleStudent,
	})
	var se *babble.ServerError
	if !errors.As(err, &se) {
		t.Fatalf("want ServerError, got %v", err)
	}
	if strings.Contains(se.Error(), "contacts is down") {
		t.Fatal("internal details leaked to the client")
	}
}

// x-babble-public: интерсептор закрывает всё, что не помечено публичным.
func TestAuthInterceptorUsesContractFlag(t *testing.T) {
	requireAuth := func(desc babble.MethodDescriptor, next babble.ServerHandler) babble.ServerHandler {
		return func(ctx context.Context, in any) (any, error) {
			if desc.Public || babble.Metadata(ctx).Get("Authorization") != "" {
				return next(ctx, in)
			}
			return nil, babble.NewValidationError("authorization is required for %s", desc.Name)
		}
	}
	ts, _ := newServer(t, &fakeContacts{}, babble.WithServerInterceptor(requireAuth))

	resp := post(t, ts.URL+"/getById", `{"id":1}`)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("want 400 without authorization, got %d (%s)", resp.code, resp.body)
	}
}

type response struct {
	code int
	body string
}

func post(t *testing.T, url, body string) response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{code: resp.StatusCode, body: strings.TrimSpace(string(raw))}
}
