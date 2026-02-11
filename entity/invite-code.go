package entity

import "time"

// InviteCode allows admins to generate one-time registration links.
// Users open a deep link (t.me/bot?start=CODE) which auto-approves them.
// UseInviteCode atomically increments UseCount and checks against MaxUses.
type InviteCode struct {
	Code      string    `bson:"code"`
	CreatedBy int64     `bson:"created_by"`
	CreatedAt time.Time `bson:"created_at"`
	UsedBy    int64     `bson:"used_by"`
	UsedAt    time.Time `bson:"used_at,omitempty"`
	MaxUses   int       `bson:"max_uses"`
	UseCount  int       `bson:"use_count"`
}
