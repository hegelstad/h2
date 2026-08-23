package bridgeservice

import (
	"os"

	"h2/internal/bridge"
	"h2/internal/bridge/macos_notify"
	"h2/internal/bridge/telegram"
	"h2/internal/config"
)

// FromConfig instantiates bridge instances from a user's bridge configuration.
func FromConfig(cfg *config.BridgesConfig) []bridge.Bridge {
	var bridges []bridge.Bridge
	if cfg.Telegram != nil {
		bridges = append(bridges, &telegram.Telegram{
			// The token is a secret and is normally kept out of config.yaml
			// as a ${TELEGRAM_BOT_TOKEN} reference into the environment.
			Token:           os.ExpandEnv(cfg.Telegram.BotToken),
			ChatID:          cfg.Telegram.ChatID,
			AllowedCommands: cfg.Telegram.AllowedCommands,
		})
	}
	if cfg.MacOSNotify != nil && cfg.MacOSNotify.Enabled {
		bridges = append(bridges, &macos_notify.MacOSNotify{})
	}
	return bridges
}
