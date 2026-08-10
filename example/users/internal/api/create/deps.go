package create

import (
	"context"

	"github.com/mosdev-tech/babble/users/internal/generated/clients/contacts"
	"github.com/mosdev-tech/babble/users/internal/generated/service/dto"
)

// Зависимости объявляются интерфейсами здесь, рядом с потребителем: хендлер
// тестируется на стабах, а конкретные реализации инъектятся в cmd/service.

type Store interface {
	ByPhone(phone string) (dto.UserEntity, bool)
	Create(phone string, role dto.UserEntityRole, firstName, lastName *string) dto.UserEntity
}

type Contacts interface {
	Sync(ctx context.Context, in *contacts.SyncIn) (*contacts.SyncOut, error)
}
