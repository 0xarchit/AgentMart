// The user binary handles Telegram commands and delegates purchase policy.
package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"agentmart/internal/telegram"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := telegram.NewClient(os.Getenv("TELEGRAM_BOT_TOKEN"), nil)
	if err != nil {
		logger.Error("user configuration failed", "error", err)
		return
	}
	ctx := context.Background()
	offset := 0
	for {
		updates, err := client.Poll(ctx, offset)
		if err != nil {
			logger.Error("telegram polling failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.Message == nil || strings.TrimSpace(update.Message.Text) == "" {
				continue
			}
			if err := handleMessage(ctx, client, update.Message); err != nil {
				logger.Error("telegram command failed", "error", err, "update_id", update.UpdateID)
			}
		}
	}
}

func handleMessage(ctx context.Context, client *telegram.Client, message *telegram.Message) error {
	command := strings.Fields(strings.TrimSpace(message.Text))
	if len(command) == 0 {
		return nil
	}
	response := responseForCommand(command[0])
	return client.SendMessage(ctx, message.Chat.ID, response)
}

func responseForCommand(command string) string {
	switch command {
	case "/start":
		return "Welcome to AgentMart. Use /link to connect your dashboard or /buy to inspect a purchase."
	case "/link":
		return "Linking is ready for the dashboard token flow. Open the dashboard to generate a token."
	case "/buy":
		return "Purchase checks are ready, but wallet fulfillment is not enabled for this account yet."
	default:
		return "Use /start, /link, or /buy."
	}
}
