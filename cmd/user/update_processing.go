// Update processing preserves Telegram updates until their handlers succeed.
package main

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/telegram"
)

func processUpdates(ctx context.Context, updates []telegram.Update, offset int, checkpoints telegramOffsetStore, process func(context.Context, *telegram.Message) error) (int, error) {
	for _, update := range updates {
		if update.UpdateID < offset {
			continue
		}
		message := messageFrom(update)
		if message != nil {
			_ = process(ctx, message)
		}
		nextOffset := update.UpdateID + 1
		if err := checkpoints.Save(ctx, nextOffset); err != nil {
			return offset, fmt.Errorf("save update offset: %w", err)
		}
		offset = nextOffset
	}
	return offset, nil
}

// messageFrom returns the message an update carries, with a tapped button turned
// into the command it stands for, and nil when there is nothing to act on. Both
// ways in share it, so a button behaves the same whether the update was posted to
// us or fetched.
func messageFrom(update telegram.Update) *telegram.Message {
	message := update.Message
	if update.CallbackQuery != nil && update.CallbackQuery.Message != nil {
		tapped := *update.CallbackQuery.Message
		tapped.From = update.CallbackQuery.From
		tapped.Text = update.CallbackQuery.Data
		tapped.CallbackQueryID = update.CallbackQuery.ID
		message = &tapped
	}
	if message == nil || strings.TrimSpace(message.Text) == "" {
		return nil
	}
	return message
}
