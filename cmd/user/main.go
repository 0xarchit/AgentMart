// The user binary handles Telegram commands and delegates purchase policy.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentmart/internal/agentloop"
	"agentmart/internal/buyer"
	"agentmart/internal/catalog"
	"agentmart/internal/gate"
	"agentmart/internal/linking"
	"agentmart/internal/marketauth"
	"agentmart/internal/marketclient"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
	"agentmart/internal/razorpay"
	buyerreasoning "agentmart/internal/reasoning"
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

type negotiator interface {
	Propose(context.Context, string, int) (negotiationclient.Proposal, error)
	Accept(context.Context, string) (negotiationclient.Resolution, error)
	Decline(context.Context, string, string) (negotiationclient.Resolution, error)
	Counter(context.Context, string, int64) (negotiationclient.Resolution, error)
}

type decisionMaker interface {
	Decide(context.Context, buyerreasoning.Input) (buyerreasoning.Decision, error)
}

type reasoningAuditor interface {
	RecordReasoningDecision(context.Context, int64, buyerreasoning.Input, buyerreasoning.Decision) error
}

type accountFactsReader interface {
	AccountForTelegram(context.Context, int64) (buyer.Account, error)
}

type productFactsReader interface {
	Get(context.Context, string) (catalog.Product, error)
}

type commandServices struct {
	negotiations negotiator
	reasoning    decisionMaker
	audit        reasoningAuditor
	accounts     accountFactsReader
	catalog      productFactsReader
	loop         *agentloop.Service
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
	var catalogReader interface {
		Get(context.Context, string) (catalog.Product, error)
		Search(context.Context, catalog.SearchRequest) ([]catalog.Product, error)
	}
	var closeCatalog func() error
	marketEndpoint := strings.TrimSpace(os.Getenv("USER_MARKET_MCP_ENDPOINT"))
	if marketEndpoint != "" {
		marketHTTP, clientErr := marketauth.NewClient(os.Getenv("MARKET_SHARED_TOKEN"), &http.Client{Timeout: 10 * time.Second})
		if clientErr != nil {
			logger.Error("market access configuration failed", "error", clientErr)
			return
		}
		merchantCatalog, connectErr := marketclient.New(ctx, marketEndpoint, marketHTTP)
		if connectErr != nil {
			logger.Error("merchant catalog connection failed", "error", connectErr)
			return
		}
		catalogReader = merchantCatalog
		closeCatalog = merchantCatalog.Close
	} else {
		catalogReader = catalog.NewService(db)
	}
	if closeCatalog != nil {
		defer func() {
			if closeErr := closeCatalog(); closeErr != nil {
				logger.Error("merchant catalog close failed", "error", closeErr)
			}
		}()
	}
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
	purchaseService := buyer.NewPurchaseService(catalogReader, store, gateService, artifactClient, wallet.NewService(db), buyer.NewApprovalStore(db))
	refundService := buyer.NewRefundService(store, wallet.NewService(db))
	reasoningService, err := buyerreasoning.New(ctx, buyerreasoning.FromEnv())
	if err != nil {
		logger.Error("user reasoning configuration failed", "error", err)
		return
	}
	var negotiationService negotiator
	negotiationEndpoint := strings.TrimSpace(os.Getenv("USER_MARKET_A2A_ENDPOINT"))
	if negotiationEndpoint != "" {
		marketHTTP, clientErr := marketauth.NewClient(os.Getenv("MARKET_SHARED_TOKEN"), &http.Client{Timeout: 10 * time.Second})
		if clientErr != nil {
			logger.Error("market access configuration failed", "error", clientErr)
			return
		}
		merchantNegotiation, connectErr := negotiationclient.NewA2A(ctx, negotiationEndpoint, marketHTTP)
		if connectErr != nil {
			logger.Error("merchant negotiation configuration failed", "error", connectErr)
			return
		}
		defer func() {
			if closeErr := merchantNegotiation.Close(); closeErr != nil {
				logger.Error("merchant negotiation close failed", "error", closeErr)
			}
		}()
		negotiationService = merchantNegotiation
	}
	var loopService *agentloop.Service
	if negotiationService != nil {
		loopTools := agentloop.Tools{
			Search: func(ctx context.Context, query string, maxPaise int64) ([]catalog.Product, error) {
				return catalogReader.Search(ctx, catalog.SearchRequest{Query: query, MaxPricePaise: maxPaise})
			},
			Get: func(ctx context.Context, id string) (catalog.Product, error) {
				return catalogReader.Get(ctx, id)
			},
			Offers: negotiationService.Propose,
			Counter: func(ctx context.Context, sessionID string, paise int64) (negotiationclient.Resolution, error) {
				return negotiationService.Counter(ctx, sessionID, paise)
			},
		}
		loopService, err = agentloop.New(ctx, buyerreasoning.FromEnv(), loopTools)
		if err != nil {
			logger.Error("buyer agent loop configuration failed", "error", err)
			return
		}
	}
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
		offset, err = processUpdates(pollContext, updates, offset, checkpoints, func(ctx context.Context, message *telegram.Message) error {
			err := handleMessage(ctx, client, linker, purchaseService, refundService, commandServices{negotiations: negotiationService, reasoning: reasoningService, audit: store, accounts: store, catalog: catalogReader, loop: loopService}, message)
			if err != nil {
				_ = client.SendMessage(ctx, message.Chat.ID, "We could not process that request. It was recorded for review.")
				_ = store.RecordUpdateDeadLetter(ctx, message.From.ID, message.Text, err)
			}
			return nil
		})
		if err != nil {
			logger.Error("telegram update processing failed", "error", err)
		}
	}
}

type loggingLinker struct{ inner linkRedeemer }

func (l loggingLinker) Redeem(ctx context.Context, token string, telegramID int64) (string, error) {
	result, err := l.inner.Redeem(ctx, token, telegramID)
	if err != nil {
		log.Printf("link error: %v", err)
	}
	return result, err
}

type loggingPurchaser struct{ inner purchaser }

func (p loggingPurchaser) Purchase(ctx context.Context, request buyer.PurchaseRequest) (buyer.PurchaseResult, error) {
	result, err := p.inner.Purchase(ctx, request)
	if err != nil {
		log.Printf("purchase error: %v", err)
	}
	return result, err
}

func (p loggingPurchaser) ResolveApproval(ctx context.Context, telegramID int64, token, decision string) (buyer.PurchaseResult, error) {
	result, err := p.inner.(approvalResolver).ResolveApproval(ctx, telegramID, token, decision)
	if err != nil {
		log.Printf("approval error: %v", err)
	}
	return result, err
}

type loggingRefunder struct{ inner refunder }

func (r loggingRefunder) Refund(ctx context.Context, request buyer.RefundRequest) (buyer.RefundResult, error) {
	result, err := r.inner.Refund(ctx, request)
	if err != nil {
		log.Printf("refund error: %v", err)
	}
	return result, err
}

type loggingNegotiator struct{ inner negotiator }

func (n loggingNegotiator) Propose(ctx context.Context, productID string, quantity int) (negotiationclient.Proposal, error) {
	result, err := n.inner.Propose(ctx, productID, quantity)
	if err != nil {
		log.Printf("negotiation propose error: %v", err)
	}
	return result, err
}

func (n loggingNegotiator) Accept(ctx context.Context, sessionID string) (negotiationclient.Resolution, error) {
	result, err := n.inner.Accept(ctx, sessionID)
	if err != nil {
		log.Printf("negotiation accept error: %v", err)
	}
	return result, err
}

func (n loggingNegotiator) Decline(ctx context.Context, sessionID, reason string) (negotiationclient.Resolution, error) {
	result, err := n.inner.Decline(ctx, sessionID, reason)
	if err != nil {
		log.Printf("negotiation decline error: %v", err)
	}
	return result, err
}

func (n loggingNegotiator) Counter(ctx context.Context, sessionID string, amountPaise int64) (negotiationclient.Resolution, error) {
	result, err := n.inner.Counter(ctx, sessionID, amountPaise)
	if err != nil {
		log.Printf("negotiation counter error: %v", err)
	}
	return result, err
}

func handleMessage(ctx context.Context, client *telegram.Client, linker linkRedeemer, purchases purchaser, refunds refunder, services commandServices, message *telegram.Message) error {
	linker = loggingLinker{inner: linker}
	purchases = loggingPurchaser{inner: purchases}
	refunds = loggingRefunder{inner: refunds}
	if services.negotiations != nil {
		services.negotiations = loggingNegotiator{inner: services.negotiations}
	}
	text := strings.TrimSpace(message.Text)
	if text != "" && !strings.HasPrefix(text, "/") {
		return conversationalBuy(ctx, client, purchases, services, message)
	}
	command := strings.Fields(text)
	if len(command) == 0 {
		return nil
	}
	response, err := responseForCommandWithServices(ctx, linker, purchases, refunds, message.From.ID, message.MessageID, command, services)
	if err != nil {
		return err
	}
	if message.CallbackQueryID != "" {
		if err := client.AnswerCallbackQuery(ctx, message.CallbackQueryID); err != nil {
			return err
		}
	}
	return client.SendMessageWithMarkup(ctx, message.Chat.ID, response, replyMarkupForResponse(response))
}

// conversationalBuy routes free-text requests ("buy me a trimmer under 2500")
// through the bounded agent loop, then executes the settled decision through
// the same Gate-guarded purchase path as every other command.
func conversationalBuy(ctx context.Context, client *telegram.Client, purchases purchaser, services commandServices, message *telegram.Message) error {
	if services.loop == nil || services.accounts == nil || services.catalog == nil || services.negotiations == nil {
		return client.SendMessage(ctx, message.Chat.ID, "The shopping agent is not configured here. Use /start to see available commands.")
	}
	account, accountErr := services.accounts.AccountForTelegram(ctx, message.From.ID)
	if accountErr != nil {
		log.Printf("agent loop account lookup failed: %v", accountErr)
		return client.SendMessage(ctx, message.Chat.ID, "Link your account first: generate a token on the dashboard website, then send /link TOKEN.")
	}
	result := services.loop.Run(ctx, message.Text, agentloop.WalletFacts{
		BalancePaise:    account.WalletBalancePaise,
		SpendLimitPaise: account.SpendLimitPaise,
	})
	var summary strings.Builder
	fmt.Fprintf(&summary, "Agent decision: %s\n%s", result.Action, result.Rationale)
	for i, step := range result.Steps {
		if i >= len(result.Steps)-3 && i < len(result.Steps) {
			fmt.Fprintf(&summary, "\n- %s", step)
		}
	}
	if result.Action == agentloop.ActionDecline {
		return client.SendMessage(ctx, message.Chat.ID, summary.String())
	}
	baseAmount := result.Product.PricePaise * int64(result.Quantity)
	purchase, err := purchases.Purchase(ctx, buyer.PurchaseRequest{
		TelegramID:       message.From.ID,
		ProductID:        result.Product.ID,
		Quantity:         result.Quantity,
		BaseAmountPaise:  baseAmount,
		FinalAmountPaise: result.FinalPaise,
		IdempotencyKey:   fmt.Sprintf("telegram:nl:%d:%d", message.From.ID, message.MessageID),
	})
	if err != nil {
		return fmt.Errorf("agentic purchase failed: %w", err)
	}
	if purchase.ApprovalRequired {
		approval := fmt.Sprintf("Human approval required for INR %.2f. Approval token: %s", float64(purchase.AmountPaise)/100, purchase.ApprovalToken)
		approval += "\n\n" + summary.String()
		if len(result.Transcript) > 0 {
			_ = client.SendDocument(ctx, message.Chat.ID, transcriptFileName(result.SessionID), renderTranscript(result.Transcript))
		}
		return client.SendMessageWithMarkup(ctx, message.Chat.ID, approval, replyMarkupForResponse(approval))
	}
	if !purchase.Fulfilled {
		summary.WriteString("\nPurchase rejected: " + purchase.Reason)
		return client.SendMessage(ctx, message.Chat.ID, summary.String())
	}
	summary.WriteString(fmt.Sprintf("\nPurchase fulfilled via wallet for INR %.2f. Audit order: %s", float64(purchase.AmountPaise)/100, purchase.RazorpayOrderID))
	if len(result.Transcript) > 0 {
		if docErr := client.SendDocument(ctx, message.Chat.ID, transcriptFileName(result.SessionID), renderTranscript(result.Transcript)); docErr != nil {
			log.Printf("send negotiation transcript failed: %v", docErr)
		}
	}
	return client.SendMessageWithMarkup(ctx, message.Chat.ID, summary.String(), replyMarkupForResponse(summary.String()))
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func transcriptFileName(sessionID string) string {
	return fmt.Sprintf("negotiation_%s.txt", short(sessionID))
}

func renderTranscript(turns []negotiation.Turn) string {
	var builder strings.Builder
	builder.WriteString("AgentMart A2A negotiation transcript\n")
	for _, turn := range turns {
		builder.WriteString(turn.At.Format("15:04:05"))
		builder.WriteString(" [")
		builder.WriteString(turn.Actor)
		builder.WriteString("] ")
		builder.WriteString(turn.Message)
		builder.WriteString("\n")
	}
	return builder.String()
}

func replyMarkupForResponse(response string) *telegram.InlineKeyboardMarkup {
	if prefix := "Human approval required"; strings.HasPrefix(response, prefix) {
		token := strings.TrimPrefix(response[strings.LastIndex(response, ":")+1:], " ")
		return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: "Approve", CallbackData: "/approve " + token},
			{Text: "Decline", CallbackData: "/reject " + token},
		}}}
	}
	if marker := "Audit order: "; strings.Contains(response, marker) {
		orderID := strings.TrimSpace(strings.SplitN(response[strings.Index(response, marker)+len(marker):], " ", 2)[0])
		return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: "Cancel Order", CallbackData: "/refund " + orderID + " Cancelled by user"},
		}}}
	}
	return nil
}

func responseForCommand(ctx context.Context, linker linkRedeemer, purchases purchaser, refunds refunder, telegramID int64, messageID int, command []string, negotiationServices ...negotiator) (string, error) {
	var services commandServices
	if len(negotiationServices) > 0 {
		services.negotiations = negotiationServices[0]
	}
	return responseForCommandWithServices(ctx, linker, purchases, refunds, telegramID, messageID, command, services)
}

func responseForCommandWithServices(ctx context.Context, linker linkRedeemer, purchases purchaser, refunds refunder, telegramID int64, messageID int, command []string, services commandServices) (string, error) {
	if len(command) == 0 {
		return "", nil
	}
	negotiations := services.negotiations
	switch command[0] {
	case "/start", "/help":
		return "Welcome to AgentMart. Just tell me what to buy (e.g. buy me a trimmer under 2500), or use /link TOKEN, /buy PRODUCT_ID QUANTITY, /negotiate PRODUCT_ID QUANTITY, /shop PRODUCT_ID QTY MAX_PAISE, /refund ORDER_ID REASON.", nil
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
	case "/negotiate":
		if negotiations == nil {
			return "Merchant negotiation is unavailable.", nil
		}
		if len(command) != 3 {
			return "Use /negotiate PRODUCT_ID QUANTITY.", nil
		}
		quantity, err := strconv.Atoi(command[2])
		if err != nil || quantity <= 0 {
			return "Quantity must be a positive integer.", nil
		}
		proposal, err := negotiations.Propose(ctx, command[1], quantity)
		if err != nil {
			return "Merchant negotiation could not be started.", nil
		}
		return fmt.Sprintf("Merchant counter offer: INR %.2f for %d unit(s). Reason: %s. Session: %s. Use /accept %s or /decline %s.", float64(proposal.FinalAmountPaise)/100, proposal.Quantity, proposal.Reason, proposal.SessionID, proposal.SessionID, proposal.SessionID), nil
	case "/accept", "/decline":
		if negotiations == nil {
			return "Merchant negotiation is unavailable.", nil
		}
		if len(command) < 2 || (command[0] == "/decline" && len(command) < 3) {
			return "Use /accept SESSION_ID or /decline SESSION_ID REASON.", nil
		}
		var resolution negotiationclient.Resolution
		var err error
		if command[0] == "/accept" {
			resolution, err = negotiations.Accept(ctx, command[1])
		} else {
			resolution, err = negotiations.Decline(ctx, command[1], strings.Join(command[2:], " "))
		}
		if err != nil {
			return "Merchant negotiation could not be resolved.", nil
		}
		if command[0] == "/decline" {
			return "Merchant counter offer declined.", nil
		}
		result, err := purchases.Purchase(ctx, buyer.PurchaseRequest{TelegramID: telegramID, ProductID: resolution.ProductID, Quantity: resolution.Quantity, BaseAmountPaise: resolution.BaseAmountPaise, FinalAmountPaise: resolution.FinalAmountPaise, IdempotencyKey: fmt.Sprintf("telegram:negotiation:%d:%s", telegramID, resolution.SessionID)})
		if err != nil {
			return "Negotiated purchase could not be completed.", nil
		}
		if result.ApprovalRequired {
			return fmt.Sprintf("Human approval required for INR %.2f. Approval token: %s", float64(result.AmountPaise)/100, result.ApprovalToken), nil
		}
		if !result.Fulfilled {
			return "Negotiated purchase rejected: " + result.Reason, nil
		}
		return fmt.Sprintf("Negotiated purchase fulfilled via wallet for INR %.2f. Audit order: %s", float64(result.AmountPaise)/100, result.RazorpayOrderID), nil
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
	case "/shop":
		return shopWithReasoning(ctx, purchases, services, telegramID, messageID, command)
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
		return "Use plain sentences like 'buy me a trimmer under 2500', or /start, /link TOKEN, /buy, /negotiate, /accept, /decline, /approve TOKEN, /reject TOKEN, /shop, or /refund ORDER_ID REASON.", nil
	}
}

func shopWithReasoning(ctx context.Context, purchases purchaser, services commandServices, telegramID int64, messageID int, command []string) (string, error) {
	if len(command) != 4 {
		return "Usage: /shop PRODUCT_ID QUANTITY MAX_PAISE", nil
	}
	if services.negotiations == nil || services.reasoning == nil || services.audit == nil || services.accounts == nil || services.catalog == nil {
		return "Shopping reasoning is not configured.", nil
	}
	quantity, err := strconv.Atoi(command[2])
	if err != nil || quantity <= 0 {
		return "Quantity must be a positive integer.", nil
	}
	limit, err := strconv.ParseInt(command[3], 10, 64)
	if err != nil || limit <= 0 {
		return "Maximum spend must be a positive amount in paise.", nil
	}
	proposal, err := services.negotiations.Propose(ctx, command[1], quantity)
	if err != nil {
		return "Merchant offer unavailable.", err
	}
	product, err := services.catalog.Get(ctx, proposal.ProductID)
	if err != nil {
		return "Catalog facts unavailable.", err
	}
	account, err := services.accounts.AccountForTelegram(ctx, telegramID)
	if err != nil {
		return "Shopping account unavailable.", err
	}
	input := buyerreasoning.Input{
		Request: strings.Join(command[1:], " "), ProductID: proposal.ProductID,
		ProductName: product.Name, Category: product.Category, Quantity: proposal.Quantity,
		Stock: product.Stock, WarrantyYears: product.WarrantyYears, TrustScore: product.TrustScore,
		ComboWith: stringValue(product.ComboWith), ComboDiscountPct: product.ComboDiscountPct,
		OfferReason:     proposal.Reason,
		BaseAmountPaise: proposal.BaseAmountPaise, FinalAmountPaise: proposal.FinalAmountPaise,
		PricePaise:  product.PricePaise,
		WalletPaise: account.WalletBalancePaise, TotalPaise: proposal.FinalAmountPaise,
		SpendLimitPaise: account.SpendLimitPaise,
	}
	if input.SpendLimitPaise <= 0 || input.SpendLimitPaise > limit {
		input.SpendLimitPaise = limit
	}
	decision, err := services.reasoning.Decide(ctx, input)
	if err != nil {
		return "Shopping decision failed.", err
	}
	if err := services.audit.RecordReasoningDecision(ctx, telegramID, input, decision); err != nil {
		return "Shopping decision could not be audited.", err
	}
	if decision.Action != buyerreasoning.ActionBuy {
		return fmt.Sprintf("Shopping decision: %s. %s", decision.Action, decision.Rationale), nil
	}
	result, err := purchases.Purchase(ctx, buyer.PurchaseRequest{TelegramID: telegramID, ProductID: proposal.ProductID, Quantity: proposal.Quantity, BaseAmountPaise: proposal.BaseAmountPaise, FinalAmountPaise: proposal.FinalAmountPaise, IdempotencyKey: fmt.Sprintf("telegram:shop:%d:%d", telegramID, messageID)})
	if err != nil {
		return "Purchase failed.", err
	}
	if result.ApprovalRequired {
		return fmt.Sprintf("Human approval required for INR %.2f. Approval token: %s", float64(result.AmountPaise)/100, result.ApprovalToken), nil
	}
	if !result.Fulfilled {
		return "Purchase was not fulfilled.", nil
	}
	return fmt.Sprintf("Reasoned purchase fulfilled via wallet for INR %.2f. Decision: %s. Audit order: %s", float64(result.AmountPaise)/100, decision.Rationale, result.RazorpayOrderID), nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
