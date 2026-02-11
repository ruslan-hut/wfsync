package bot

import (
	"wfsync/entity"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
)

// Per-role command lists for Telegram's menu button (the "/" icon in the chat input).
// These are pushed via SetMyCommands with BotCommandScopeChat to give each user
// a role-appropriate command menu. syncAllUserMenus() sets them on bot startup.

var commandsAnonymous = []tgbotapi.BotCommand{
	{Command: "start", Description: "Register or enable notifications"},
	{Command: "help", Description: "Show available commands"},
}

var commandsUser = []tgbotapi.BotCommand{
	{Command: "start", Description: "Enable notifications"},
	{Command: "stop", Description: "Disable notifications"},
	{Command: "topics", Description: "Manage topic subscriptions"},
	{Command: "tier", Description: "Set notification tier"},
	{Command: "status", Description: "Show your settings"},
	{Command: "help", Description: "Show available commands"},
}

var commandsAdmin = []tgbotapi.BotCommand{
	{Command: "start", Description: "Enable notifications"},
	{Command: "stop", Description: "Disable notifications"},
	{Command: "topics", Description: "Manage topic subscriptions"},
	{Command: "tier", Description: "Set notification tier"},
	{Command: "level", Description: "Set log level filter"},
	{Command: "status", Description: "Show your settings"},
	{Command: "users", Description: "List all users"},
	{Command: "approve", Description: "Approve a pending user"},
	{Command: "revoke", Description: "Revoke user access"},
	{Command: "admin", Description: "Promote user to admin"},
	{Command: "invite", Description: "Generate invite code"},
	{Command: "help", Description: "Show available commands"},
}

// setDefaultCommands sets the default bot menu for unknown users.
func (t *TgBot) setDefaultCommands() {
	_, err := t.api.SetMyCommands(commandsAnonymous, &tgbotapi.SetMyCommandsOpts{
		Scope: tgbotapi.BotCommandScopeDefault{},
	})
	if err != nil {
		t.log.Warn("setting default commands", "error", err)
	}
}

// setUserCommands sets the command menu for a specific user based on their role.
func (t *TgBot) setUserCommands(chatId int64, role entity.TelegramRole) {
	var commands []tgbotapi.BotCommand
	switch role {
	case entity.RoleAdmin:
		commands = commandsAdmin
	case entity.RoleUser:
		commands = commandsUser
	default:
		commands = commandsAnonymous
	}

	_, err := t.api.SetMyCommands(commands, &tgbotapi.SetMyCommandsOpts{
		Scope: tgbotapi.BotCommandScopeChat{ChatId: chatId},
	})
	if err != nil {
		t.log.Warn("setting user commands", "chat_id", chatId, "error", err)
	}
}

// syncAllUserMenus sets command menus for all known users based on their roles.
func (t *TgBot) syncAllUserMenus() {
	t.mu.RLock()
	users := make(map[int64]entity.TelegramRole, len(t.users))
	for id, u := range t.users {
		users[id] = u.TelegramRole
	}
	t.mu.RUnlock()

	for chatId, role := range users {
		t.setUserCommands(chatId, role)
	}
}
