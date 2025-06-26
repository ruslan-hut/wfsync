package auth

import (
	"fmt"
	"wfsync/entity"
)

type Database interface {
	GetUser(token string) (*entity.User, error)
}
type Auth struct {
	db Database
}

func New(db Database) *Auth {
	return &Auth{db: db}
}

func (a Auth) UserByToken(token string) (*entity.User, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	return a.db.GetUser(token)
}
