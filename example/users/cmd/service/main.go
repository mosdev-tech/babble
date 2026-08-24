package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mosdev-tech/babble"
	"github.com/mosdev-tech/babble/users/internal/api/handler/create"
	get_by_id "github.com/mosdev-tech/babble/users/internal/api/handler/get_by_id"
	"github.com/mosdev-tech/babble/users/internal/generated/clients/contacts"
	"github.com/mosdev-tech/babble/users/internal/generated/service"
	"github.com/mosdev-tech/babble/users/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	// Общие настройки клиентов собираются один раз и передаются каждому.
	clientOpts := babble.WithRecommendedClientSettings(babble.RecommendedClientSettings{
		Timeout:        300 * time.Millisecond,
		Interceptors:   []babble.ClientInterceptor{logCalls},
		ForwardHeaders: []string{"X-Request-Id"},
	})

	rpc, err := babble.ClientFor[contacts.Service](clientOpts)
	if err != nil {
		return err
	}

	st := store.New()

	srv, err := babble.NewServer(
		babble.WithSettings(babble.Settings{Address: ":8080", ShutdownTimeout: 10 * time.Second}),
		babble.WithMethod(service.Create, create.New(st, contacts.New(rpc)).Handle),
		babble.WithMethod(service.GetById, get_by_id.New(st).Handle),
		babble.WithServerInterceptor(requireAuth),
		babble.WithErrorLogger(func(_ context.Context, err error) { log.Println("error:", err) }),
	)
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

// requireAuth показывает, зачем x-babble-public живёт в контракте: закрытость —
// дефолт, и забыть закрыть новый метод невозможно.
func requireAuth(desc babble.MethodDescriptor, next babble.ServerHandler) babble.ServerHandler {
	return func(ctx context.Context, in any) (any, error) {
		if desc.Public {
			return next(ctx, in)
		}
		if babble.Metadata(ctx).Get("Authorization") == "" {
			return nil, babble.NewValidationError("authorization is required for %s", desc.Name)
		}
		return next(ctx, in)
	}
}

func logCalls(info babble.CallInfo, next babble.Caller) babble.Caller {
	return func(ctx context.Context, in any, out any) error {
		start := time.Now()
		err := next(ctx, in, out)
		log.Printf("rpc %s took %s err=%v", info.Procedure, time.Since(start), err)
		return err
	}
}
