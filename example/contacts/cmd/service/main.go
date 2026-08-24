package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mosdev-tech/babble"
	"github.com/mosdev-tech/babble/contacts/internal/api/handler/sync"
	"github.com/mosdev-tech/babble/contacts/internal/generated/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := babble.NewServer(
		babble.WithSettings(babble.Settings{Address: ":8081", ShutdownTimeout: 10 * time.Second}),
		babble.WithMethod(service.Sync, sync.New().Handle),
		babble.WithErrorLogger(func(_ context.Context, err error) { log.Println("error:", err) }),
	)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
