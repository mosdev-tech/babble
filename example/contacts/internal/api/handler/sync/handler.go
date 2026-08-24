package sync

import (
	"context"
	"fmt"
	"sync"

	"github.com/mosdev-tech/babble/contacts/internal/generated/service/dto"
)

// Handler синхронизирует контакт пользователя.
type Handler struct {
	mu       sync.Mutex
	contacts map[int64]string
}

func New() *Handler { return &Handler{contacts: map[int64]string{}} }

func (h *Handler) Handle(ctx context.Context, in *dto.SyncIn) (*dto.SyncOut, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.contacts[in.UserId] = in.Phone
	return &dto.SyncOut{ContactId: fmt.Sprintf("contact-%d", in.UserId)}, nil
}
