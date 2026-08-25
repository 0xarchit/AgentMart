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
		if update.Message != nil && strings.TrimSpace(update.Message.Text) != "" {
			_ = process(ctx, update.Message)
		}
		nextOffset := update.UpdateID + 1
		if err := checkpoints.Save(ctx, nextOffset); err != nil {
			return offset, fmt.Errorf("save update offset: %w", err)
		}
		offset = nextOffset
	}
	return offset, nil
}
