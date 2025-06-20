package cont

import (
	"context"
	"wfsync/entity"
)

type ctxKey string

const UserDataKey ctxKey = "userData"

func PutUser(c context.Context, user *entity.User) context.Context {
	return context.WithValue(c, UserDataKey, *user)
}

func GetUser(c context.Context) *entity.User {
	user, ok := c.Value(UserDataKey).(entity.User)
	if !ok {
		return &entity.User{}
	}
	return &user
}
