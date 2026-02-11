package bot

import (
	"fmt"
	"strings"
	"time"
	"wfsync/entity"

	"github.com/google/uuid"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

func (t *TgBot) usersCmd(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireAdmin(chatId) {
		t.plainResponse(chatId, "Admin access required\\.")
		return nil
	}

	t.mu.RLock()
	users := make([]*entity.User, 0, len(t.users))
	for _, u := range t.users {
		users = append(users, u)
	}
	t.mu.RUnlock()

	if len(users) == 0 {
		t.plainResponse(chatId, "No telegram users found\\.")
		return nil
	}

	// Group by role
	grouped := map[entity.TelegramRole][]*entity.User{}
	for _, u := range users {
		grouped[u.TelegramRole] = append(grouped[u.TelegramRole], u)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Users* \\(%d total\\)\n", len(users)))

	roleOrder := []entity.TelegramRole{entity.RoleAdmin, entity.RoleUser, entity.RolePending, entity.RoleNone}
	for _, role := range roleOrder {
		roleUsers, ok := grouped[role]
		if !ok || len(roleUsers) == 0 {
			continue
		}
		roleName := string(role)
		if roleName == "" {
			roleName = "none"
		}
		sb.WriteString(fmt.Sprintf("\n*%s* \\(%d\\):\n", Sanitize(roleName), len(roleUsers)))
		for _, u := range roleUsers {
			enabled := "off"
			if u.TelegramEnabled {
				enabled = "on"
			}
			tier := string(u.SubscriptionTier)
			if tier == "" {
				tier = "realtime"
			}
			topics := "all"
			if len(u.TelegramTopics) > 0 {
				topics = strings.Join(u.TelegramTopics, ",")
			}
			sb.WriteString(fmt.Sprintf("  %s \\| %s \\| tier:%s \\| topics:%s\n",
				Sanitize(userDisplayName(u)),
				Sanitize(enabled),
				Sanitize(tier),
				Sanitize(topics),
			))
		}
	}

	parts := splitMessage(sb.String(), maxTelegramMessageLen)
	for _, part := range parts {
		t.plainResponse(chatId, part)
	}
	return nil
}

func (t *TgBot) approve(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireAdmin(chatId) {
		t.plainResponse(chatId, "Admin access required\\.")
		return nil
	}

	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		t.plainResponse(chatId, "Usage: `/approve <id|@username>`")
		return nil
	}

	target := t.resolveUser(args[1])
	if target == nil {
		t.plainResponse(chatId, "User not found: "+Sanitize(args[1]))
		return nil
	}

	err := t.db.SetTelegramRole(target.TelegramId, entity.RoleUser)
	if err != nil {
		t.plainResponse(chatId, "Error: "+Sanitize(err.Error()))
		return nil
	}

	t.plainResponse(chatId, "User "+Sanitize(userDisplayName(target))+" approved\\.")
	t.plainResponse(target.TelegramId, "Your registration has been approved\\! Use `/start` to enable notifications\\.")
	t.loadUsers()
	return nil
}

func (t *TgBot) revoke(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireAdmin(chatId) {
		t.plainResponse(chatId, "Admin access required\\.")
		return nil
	}

	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		t.plainResponse(chatId, "Usage: `/revoke <id|@username>`")
		return nil
	}

	target := t.resolveUser(args[1])
	if target == nil {
		t.plainResponse(chatId, "User not found: "+Sanitize(args[1]))
		return nil
	}

	err := t.db.SetTelegramRole(target.TelegramId, entity.RoleNone)
	if err != nil {
		t.plainResponse(chatId, "Error: "+Sanitize(err.Error()))
		return nil
	}

	t.plainResponse(chatId, "User "+Sanitize(userDisplayName(target))+" revoked\\.")
	t.plainResponse(target.TelegramId, "Your access has been revoked\\.")
	t.loadUsers()
	return nil
}

func (t *TgBot) adminCmd(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireAdmin(chatId) {
		t.plainResponse(chatId, "Admin access required\\.")
		return nil
	}

	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		t.plainResponse(chatId, "Usage: `/admin <id|@username>`")
		return nil
	}

	target := t.resolveUser(args[1])
	if target == nil {
		t.plainResponse(chatId, "User not found: "+Sanitize(args[1]))
		return nil
	}

	if !target.IsApproved() {
		t.plainResponse(chatId, "User must be approved first\\.")
		return nil
	}

	err := t.db.SetTelegramRole(target.TelegramId, entity.RoleAdmin)
	if err != nil {
		t.plainResponse(chatId, "Error: "+Sanitize(err.Error()))
		return nil
	}

	t.plainResponse(chatId, "User "+Sanitize(userDisplayName(target))+" promoted to admin\\.")
	t.plainResponse(target.TelegramId, "You have been promoted to admin\\!")
	t.loadUsers()
	return nil
}

func (t *TgBot) invite(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireAdmin(chatId) {
		t.plainResponse(chatId, "Admin access required\\.")
		return nil
	}

	code := uuid.New().String()[:t.config.InviteCodeLength]

	inviteCode := &entity.InviteCode{
		Code:      code,
		CreatedBy: chatId,
		CreatedAt: time.Now(),
		MaxUses:   1,
		UseCount:  0,
	}

	err := t.db.CreateInviteCode(inviteCode)
	if err != nil {
		t.plainResponse(chatId, "Error creating invite code: "+Sanitize(err.Error()))
		return nil
	}

	botUsername := t.api.Username
	deepLink := fmt.Sprintf("https://t.me/%s?start=%s", botUsername, code)
	t.plainResponse(chatId, fmt.Sprintf("Invite code: `%s`\nDeep link: %s", Sanitize(code), Sanitize(deepLink)))
	return nil
}
