package create

import (
	"context"
	"fmt"

	"github.com/mosdev-tech/babble/users/internal/generated/clients/contacts"
	"github.com/mosdev-tech/babble/users/internal/generated/service/dto"
)

// Handler создаёт нового пользователя.
type Handler struct {
	store    Store
	contacts Contacts
}

func New(store Store, contacts Contacts) *Handler {
	return &Handler{store: store, contacts: contacts}
}

func (h *Handler) Handle(ctx context.Context, in *dto.CreateIn) (*dto.CreateOut, error) {
	// Бизнес-исходы — поля в Out, а не коды HTTP: транспорт знает только
	// успех, ValidationError и ServerError.
	if existing, ok := h.store.ByPhone(in.Phone); ok {
		return &dto.CreateOut{
			Ok:    false,
			Error: dto.NewCreateErrorPhoneTaken(dto.PhoneTakenError{ExistingUserId: existing.Id}),
		}, nil
	}
	if in.Role == dto.CreateInRoleAdmin {
		return &dto.CreateOut{
			Ok:    false,
			Error: dto.NewCreateErrorRoleNotAllowed(dto.RoleNotAllowedError{Role: dto.RoleNotAllowedErrorRoleAdmin}),
		}, nil
	}

	user := h.store.Create(in.Phone, dto.UserEntityRole(in.Role), in.FirstName, in.LastName)

	out, err := h.contacts.Sync(ctx, &contacts.SyncIn{UserId: user.Id, Phone: user.Phone})
	if err != nil {
		// Зависимость не ответила — это ServerError, наружу уйдёт 500.
		return nil, fmt.Errorf("sync contact for user %d: %w", user.Id, err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("contacts rejected user %d", user.Id)
	}

	return &dto.CreateOut{Ok: true, User: &user}, nil
}
