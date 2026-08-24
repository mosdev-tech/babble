package get_by_id

import (
	"context"

	"github.com/mosdev-tech/babble/users/internal/generated/service/dto"
)

type Store interface {
	ByID(id int64) (dto.UserEntity, bool)
}

// Handler возвращает пользователя по идентификатору.
type Handler struct {
	store Store
}

func New(store Store) *Handler { return &Handler{store: store} }

func (h *Handler) Handle(ctx context.Context, in *dto.GetByIdIn) (*dto.GetByIdOut, error) {
	user, ok := h.store.ByID(in.Id)
	if !ok {
		// «Не нашли» — обычный исход, а не ошибка транспорта.
		return &dto.GetByIdOut{Found: false}, nil
	}
	return &dto.GetByIdOut{Found: true, User: &user}, nil
}
