// The user binary handles Telegram commands and delegates purchase policy.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentmart/internal/buyer"
	"agentmart/internal/catalog"
	"agentmart/internal/gate"
	"agentmart/internal/linking"
	"agentmart/internal/negotiation"
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

type refunder interface {
	Refund(context.Context, buyer.RefundRequest) (buyer.RefundResult, error)
}

type approvalResolver interface {
	ResolveApproval(context.Context, int64, string, string) (buyer.PurchaseResult, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	purchaseService := buyer.NewPurchaseService(catalogService, store, gateService, artifactClient, wallet.NewService(db), buyer.NewApprovalStore(db))
	refundService := buyer.NewRefundService(store, wallet.NewService(db))
	pollContext := ctx
	offset := 0
	var checkpoints telegramOffsetStore
	redisURL := strings.TrimSpace(os.Getenv("UPSTASH_REDIS_REST_URL"))
	redisToken := strings.TrimSpace(os.Getenv("UPSTASH_REDIS_REST_TOKEN"))
	if redisURL != "" && redisToken != "" {
		redisStore, storeErr := negotiation.NewRedisSessionStore(redisURL, redisToken, &http.Client{Timeout: 10 * time.Second})
		if storeErr != nil {
			logger.Error("telegram offset store configuration failed", "error", storeErr)
			return
		}
		checkpoints = redisOffsetStore{store: redisStore}
	} else {
		checkpoints = newOffsetStore(os.Getenv("TELEGRAM_OFFSET_FILE"))
	}
	offset, err = checkpoints.Load(pollContext)
	if err != nil {
		logger.Error("telegram offset load failed", "error", err)
		return
	}
	for {
		updates, err := client.Poll(pollContext, offset)
		if err != nil {
			if pollContext.Err() != nil {
				return
			}
			logger.Error("telegram polling failed", "error", err)
			continue
		}
		for _, update := range updates {
			if update.UpdateID < offset {
				continue
			}
			if update.Message != nil && strings.TrimSpace(update.Message.Text) != "" {
				if err := handleMessage(pollContext, client, linker, purchaseService, refundService, update.Message); err != nil {
					logger.Error("telegram message handling failed", "error", err)
					continue
				}
			}
			offset = update.UpdateID + 1
			if err := checkpoints.Save(pollContext, offset); err != nil {
				logger.Error("telegram offset save failed", "error", err)
				return
			}
		}
	}
}

func handleMessage(ctx context.Context, client *telegram.Client, linker linkRedeemer, purchases purchaser, refunds refunder, message *telegram.Message) error {
	command := strings.Fields(strings.TrimSpace(message.Text))
	if len(command) == 0 {
		return nil
	}
	response, err := responseForCommand(ctx, linker, purchases, refunds, message.From.ID, message.MessageID, command)
	if err != nil {
		return err
	}
	return client.SendMessage(ctx, message.Chat.ID, response)
}

func responseForCommand(ctx context.Context, linker linkRedeemer, purchases purchaser, refunds refunder, telegramID int64, messageID int, command []string) (string, error) {
	switch command[0] {
	case "/start":
		return "Welcome to AgentMart. Use /link TOKEN, /buy PRODUCT_ID QUANTITY, or /refund ORDER_ID REASON.", nil
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
			if result.ApprovalRequired {
				return fmt.Sprintf("Human approval required for INR %.2f. Approval token: %s", float64(result.AmountPaise)/100, result.ApprovalToken), nil
			}
			return "Purchase rejected: " + result.Reason, nil
		}
		return fmt.Sprintf("Purchase fulfilled via wallet for INR %.2f. Audit order: %s", float64(result.AmountPaise)/100, result.RazorpayOrderID), nil
	case "/approve", "/reject":
		if len(command) != 2 {
			return "Use /approve TOKEN or /reject TOKEN.", nil
		}
		resolver, ok := purchases.(approvalResolver)
		if !ok {
			return "Approval resume is unavailable.", nil
		}
		decision := strings.TrimPrefix(command[0], "/")
		result, err := resolver.ResolveApproval(ctx, telegramID, command[1], decision)
		if err != nil {
			return "Approval could not be processed.", nil
		}
		if !result.Fulfilled {
			return "Approval result: " + result.Reason, nil
		}
		return fmt.Sprintf("Purchase fulfilled via wallet for INR %.2f. Audit order: %s", float64(result.AmountPaise)/100, result.RazorpayOrderID), nil
	case "/refund":
		if len(command) < 3 {
			return "Use /refund ORDER_ID REASON.", nil
		}
		result, err := refunds.Refund(ctx, buyer.RefundRequest{TelegramID: telegramID, MessageID: messageID, OrderID: command[1], Reason: strings.Join(command[2:], " ")})
		if err != nil {
			return "Refund could not be processed. Check the order and try again.", nil
		}
		if result.Duplicate {
			return fmt.Sprintf("Refund already applied for order %s.", result.OrderID), nil
		}
		if !result.Approved {
			return "Refund rejected: " + result.Reason, nil
		}
		return fmt.Sprintf("Refund approved via wallet for INR %.2f. Order: %s", float64(result.AmountPaise)/100, result.OrderID), nil
	default:
		return "Use /start, /link TOKEN, /buy, /approve TOKEN, /reject TOKEN, or /refund ORDER_ID REASON.", nil
	}
}
