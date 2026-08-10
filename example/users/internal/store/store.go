// Package store — учебное хранилище пользователей в памяти.
package store

import (
	"sync"
	"time"

	"github.com/mosdev-tech/babble/users/internal/generated/service/dto"
)

type Store struct {
	mu     sync.Mutex
	nextID int64
	byID   map[int64]dto.UserEntity
	byUser map[string]int64
	now    func() time.Time
}

func New() *Store {
	return &Store{
		nextID: 1,
		byID:   map[int64]dto.UserEntity{},
		byUser: map[string]int64{},
		now:    time.Now,
	}
}

// WithClock подменяет часы — тестам нужен воспроизводимый createdAt.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

func (s *Store) ByPhone(phone string) (dto.UserEntity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byUser[phone]
	if !ok {
		return dto.UserEntity{}, false
	}
	return s.byID[id], true
}

func (s *Store) ByID(id int64) (dto.UserEntity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	return u, ok
}

func (s *Store) Create(phone string, role dto.UserEntityRole, firstName, lastName *string) dto.UserEntity {
	s.mu.Lock()
	defer s.mu.Unlock()

	user := dto.UserEntity{
		Id:        s.nextID,
		Phone:     phone,
		Role:      role,
		FirstName: firstName,
		LastName:  lastName,
		CreatedAt: s.now().UTC(),
	}
	s.byID[user.Id] = user
	s.byUser[phone] = user.Id
	s.nextID++
	return user
}
