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

// usersCmd lists all registered Telegram users, grouped by role.
// Sends approve/revoke inline buttons for each pending user.
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
	// Collect pending users to show with action buttons
	var pendingUsers []*entity.User
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
			if role == entity.RolePending {
				pendingUsers = append(pendingUsers, u)
			}
		}
	}

	parts := splitMessage(sb.String(), maxTelegramMessageLen)
	for _, part := range parts {
		t.plainResponse(chatId, part)
	}

	// Send individual messages with approve/revoke buttons for each pending user
	for _, u := range pendingUsers {
		keyboard := buildPendingUserButtons(u.TelegramId)
		t.sendWithKeyboard(chatId,
			fmt.Sprintf("Pending: %s", Sanitize(userDisplayName(u))),
			keyboard,
		)
	}
	return nil
}

// approve sets a user's role to RoleUser, enables notifications, and assigns default topics.
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
		t.reportError(chatId, "/approve", err)
		return nil
	}

	// Set default topic to invoice only for new users
	_ = t.db.SetTelegramTopics(target.TelegramId, []string{entity.TopicInvoice})

	t.plainResponse(chatId, "User "+Sanitize(userDisplayName(target))+" approved\\.")
	t.plainResponse(target.TelegramId, "Your registration has been approved\\! Notifications are now enabled\\.")
	t.loadUsers()
	t.setUserCommands(target.TelegramId, entity.RoleUser)
	return nil
}

// revoke sets a user's role to RoleNone, disabling all access and notifications.
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
		t.reportError(chatId, "/revoke", err)
		return nil
	}

	t.plainResponse(chatId, "User "+Sanitize(userDisplayName(target))+" revoked\\.")
	t.plainResponse(target.TelegramId, "Your access has been revoked\\.")
	t.loadUsers()
	t.setUserCommands(target.TelegramId, entity.RoleNone)
	return nil
}

// adminCmd promotes an approved user to admin role.
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
		t.reportError(chatId, "/admin", err)
		return nil
	}

	t.plainResponse(chatId, "User "+Sanitize(userDisplayName(target))+" promoted to admin\\.")
	t.plainResponse(target.TelegramId, "You have been promoted to admin\\!")
	t.loadUsers()
	t.setUserCommands(target.TelegramId, entity.RoleAdmin)
	return nil
}

// invite generates a single-use invite code and returns a Telegram deep link.
// New users opening the deep link are auto-approved without admin intervention.
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
		t.reportError(chatId, "/invite", err)
		return nil
	}

	botUsername := t.api.Username
	deepLink := fmt.Sprintf("https://t.me/%s?start=%s", botUsername, code)
	t.plainResponse(chatId, fmt.Sprintf("Invite code: `%s`\nDeep link: %s", Sanitize(code), Sanitize(deepLink)))
	return nil
}
