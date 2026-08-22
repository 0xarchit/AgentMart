// The user binary handles Telegram commands and delegates purchase policy.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"agentmart/internal/buyer"
	"agentmart/internal/catalog"
	"agentmart/internal/gate"
	"agentmart/internal/linking"
	"agentmart/internal/razorpay"
	"agentmart/internal/supabase"
	"agentmart/internal/telegram"
	"agentmart/internal/wallet"
)

type linkRedeemer interface {
	Redeem(context.Context, string, int64) (string, error)
}

type purchaser interface {
	Purchase(context.Context, buyer.PurchaseRequest) (buyer.PurchaseResult, error)
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
	catalogService := catalog.NewService(db)
	store := buyer.NewStore(db)
	gateService, err := gate.New(store, time.Minute)
	if err != nil {
		logger.Error("user gate configuration failed", "error", err)
		return
	}
	artifactClient, err := razorpay.NewClient(os.Getenv("RAZORPAY_KEY_ID"), os.Getenv("RAZORPAY_KEY_SECRET"), &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		logger.Error("user payment configuration failed", "error", err)
		return
	}
	purchaseService := buyer.NewPurchaseService(catalogService, store, gateService, artifactClient, wallet.NewService(db))
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
			if err := handleMessage(ctx, client, linker, purchaseService, update.Message); err != nil {
				logger.Error("telegram command failed", "error", err, "update_id", update.UpdateID)
			}
		}
	}
}

func handleMessage(ctx context.Context, client *telegram.Client, linker linkRedeemer, purchases purchaser, message *telegram.Message) error {
	command := strings.Fields(strings.TrimSpace(message.Text))
	if len(command) == 0 {
		return nil
	}
	response, err := responseForCommand(ctx, linker, purchases, message.From.ID, message.MessageID, command)
	if err != nil {
		return err
	}
	return client.SendMessage(ctx, message.Chat.ID, response)
}

func responseForCommand(ctx context.Context, linker linkRedeemer, purchases purchaser, telegramID int64, messageID int, command []string) (string, error) {
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
		if len(command) != 3 {
			return "Use /buy PRODUCT_ID QUANTITY.", nil
		}
		quantity, err := strconv.Atoi(command[2])
		if err != nil || quantity <= 0 {
			return "Quantity must be a positive integer.", nil
		}
		result, err := purchases.Purchase(ctx, buyer.PurchaseRequest{TelegramID: telegramID, ProductID: command[1], Quantity: quantity, IdempotencyKey: fmt.Sprintf("telegram:%d:%d", telegramID, messageID)})
		if err != nil {
			return "Purchase could not be completed. Check the dashboard and try again.", nil
		}
		if !result.Fulfilled {
			return "Purchase rejected: " + result.Reason, nil
		}
		return fmt.Sprintf("Purchase fulfilled via wallet for INR %.2f. Audit order: %s", float64(result.AmountPaise)/100, result.RazorpayOrderID), nil
	default:
		return "Use /start, /link TOKEN, or /buy.", nil
	}
}
