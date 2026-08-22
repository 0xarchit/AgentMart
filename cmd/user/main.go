// The user binary handles Telegram commands and delegates purchase policy.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"agentmart/internal/linking"
	"agentmart/internal/supabase"
	"agentmart/internal/telegram"
)

type linkRedeemer interface {
	Redeem(context.Context, string, int64) (string, error)
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := telegram.NewClient(os.Getenv("TELEGRAM_BOT_TOKEN"), nil)
	if err != nil {
		logger.Error("user configuration failed", "error", err)
		return
	}
	db, err := supabase.NewClient(os.Getenv("SUPABASE_URL"), os.Getenv("SUPABASE_SECRET_KEY"), &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		logger.Error("user database configuration failed", "error", err)
		return
	}
	linker := linking.NewService(db)
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
			if err := handleMessage(ctx, client, linker, update.Message); err != nil {
				logger.Error("telegram command failed", "error", err, "update_id", update.UpdateID)
			}
		}
	}
}

func handleMessage(ctx context.Context, client *telegram.Client, linker linkRedeemer, message *telegram.Message) error {
	command := strings.Fields(strings.TrimSpace(message.Text))
	if len(command) == 0 {
		return nil
	}
	response, err := responseForCommand(ctx, linker, message.From.ID, command)
	if err != nil {
		return err
	}
	return client.SendMessage(ctx, message.Chat.ID, response)
}

func responseForCommand(ctx context.Context, linker linkRedeemer, telegramID int64, command []string) (string, error) {
	switch command[0] {
	case "/start":
		return "Welcome to AgentMart. Use /link TOKEN to connect your dashboard or /buy to inspect a purchase.", nil
	case "/link":
		if len(command) != 2 {
			return "Use /link TOKEN after generating a token in the dashboard.", nil
		}
		if _, err := linker.Redeem(ctx, command[1], telegramID); err != nil {
			return "That link token is invalid, expired, or already used.", nil
		}
		return "Telegram is now linked to your AgentMart wallet.", nil
	case "/buy":
		return "Purchase checks are ready, but wallet fulfillment is not enabled for this account yet.", nil
	default:
		return "Use /start, /link TOKEN, or /buy.", nil
	}
}
