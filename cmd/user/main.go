// The user binary handles Telegram commands and delegates purchase policy.
package main

import (
	"context"
	"errors"
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

	"agentmart/internal/buyer"
	"agentmart/internal/catalog"
	"agentmart/internal/failure"
	"agentmart/internal/gate"
	"agentmart/internal/health"
	"agentmart/internal/linking"
	"agentmart/internal/llmchat"
	"agentmart/internal/marketauth"
	"agentmart/internal/marketclient"
	"agentmart/internal/modelconfig"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
	"agentmart/internal/razorpay"
	"agentmart/internal/remotemerchant"
	"agentmart/internal/runid"
	"agentmart/internal/shopgraph"
	"agentmart/internal/supabase"
	"agentmart/internal/telegram"
	"agentmart/internal/wallet"
)

// Each channel gets the patience its work needs. The conversation channel waits
// on the merchant's reasoning, the tool channel only on a database read.
const (
	recordsTimeout      = 15 * time.Second
	catalogToolTimeout  = 45 * time.Second
	conversationTimeout = 300 * time.Second
	paymentTimeout      = 30 * time.Second
	// pollBackoff is how long a failed long poll waits before asking again. A bare
	// continue span the loop at full speed, so a bad token or a network outage
	// hammered the API and filled the log as fast as it could. Fixed rather than
	// exponential on purpose: the ceiling here is twelve failed requests a minute,
	// which is quiet enough, and a backoff that grows needs state and a reset rule
	// to go with it.
	pollBackoff = 5 * time.Second
)

type linkRedeemer interface {
	Redeem(context.Context, string, int64) (string, error)
}

type purchaser interface {
	Purchase(context.Context, buyer.PurchaseRequest) (buyer.PurchaseResult, error)
	RequestApproval(context.Context, buyer.PurchaseRequest, string) (buyer.PurchaseResult, error)
}

type refunder interface {
	Refund(context.Context, buyer.RefundRequest) (buyer.RefundResult, error)
}

type approvalResolver interface {
	ResolveApproval(context.Context, int64, string, string) (buyer.PurchaseResult, error)
}

type negotiator interface {
	Browse(context.Context, string, int64, string) (negotiationclient.Shortlist, error)
	Propose(context.Context, string, int) (negotiationclient.Proposal, error)
	ProposeAs(context.Context, string, int, string) (negotiationclient.Proposal, error)
	Accept(context.Context, string) (negotiationclient.Resolution, error)
	Decline(context.Context, string, string) (negotiationclient.Resolution, error)
	Counter(context.Context, string, int64) (negotiationclient.Resolution, error)
}

type reasoningAuditor interface {
	RecordAgentRun(context.Context, int64, string, buyer.AgentRun) error
}

type accountFactsReader interface {
	AccountForTelegram(context.Context, int64) (buyer.Account, error)
}

type productFactsReader interface {
	Get(context.Context, string) (catalog.Product, error)
}

// openDecisionReader finds a decision the person was asked for and has not
// answered. It can only read: nothing here resolves or spends.
type openDecisionReader interface {
	PendingFor(context.Context, int64) (buyer.PendingApproval, bool, error)
}

type commandServices struct {
	negotiations  negotiator
	health        func(context.Context) string
	audit         reasoningAuditor
	accounts      accountFactsReader
	catalog       productFactsReader
	approvals     openDecisionReader
	conversations conversationMemory
	loop          *shopgraph.Service
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
	db, err := supabase.NewClient(os.Getenv("SUPABASE_URL"), os.Getenv("SUPABASE_SECRET_KEY"), &http.Client{Timeout: recordsTimeout})
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
		marketHTTP, clientErr := marketauth.NewClient(os.Getenv("MARKET_SHARED_TOKEN"), &http.Client{Timeout: catalogToolTimeout})
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
	// The freshness window is the price rail. It was never reachable while callers
	// passed the same instant as both the observation time and now, so this value
	// only starts mattering here: it is set from the conversation budget, since a
	// quote can legitimately be a few minutes old inside one shopping run.
	gateService, err := gate.New(store, 5*time.Minute)
	if err != nil {
		logger.Error("user gate configuration failed", "error", err)
		return
	}
	artifactClient, err := razorpay.NewClient(os.Getenv("RAZORPAY_KEY_ID"), os.Getenv("RAZORPAY_KEY_SECRET"), &http.Client{Timeout: paymentTimeout})
	if err != nil {
		logger.Error("user payment configuration failed", "error", err)
		return
	}
	approvalStore := buyer.NewApprovalStore(db)
	purchaseService := buyer.NewPurchaseService(catalogReader, store, gateService, artifactClient, wallet.NewService(db), approvalStore)
	// Refusals before the gate is consulted leave a row too, so no money path in
	// the trail is silent.
	purchaseService.UseFailureTrail(store)
	refundService := buyer.NewRefundService(store, wallet.NewService(db))
	// A cancellation credits the allowance and then reverses the captured payments
	// that funded it, so the failure path leaves evidence outside our own tables.
	refundService.UseReversal(buyer.NewGatewayReversal(store, artifactClient), store)
	buyerModel := modelconfig.FromEnv("USER")
	var negotiationService negotiator
	negotiationEndpoint := strings.TrimSpace(os.Getenv("USER_MARKET_A2A_ENDPOINT"))
	if negotiationEndpoint != "" {
		marketHTTP, clientErr := marketauth.NewClient(os.Getenv("MARKET_SHARED_TOKEN"), &http.Client{Timeout: conversationTimeout})
		if clientErr != nil {
			logger.Error("market access configuration failed", "error", clientErr)
			return
		}
		merchantNegotiation, connectErr := negotiationclient.NewAgentClient(ctx, negotiationEndpoint, marketHTTP)
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
	var loopService *shopgraph.Service
	if negotiationService != nil {
		loopTools := shopgraph.Tools{
			Browse: func(ctx context.Context, brief string, budgetPaise int64, accountID string) (negotiationclient.Shortlist, error) {
				shortlist, browseErr := negotiationService.Browse(ctx, brief, budgetPaise, accountID)
				return shortlist, failure.Conversation(browseErr)
			},
			Get: func(ctx context.Context, id string) (catalog.Product, error) {
				product, getErr := catalogReader.Get(ctx, id)
				return product, failure.Catalog(getErr)
			},
			Offers: func(ctx context.Context, id string, qty int, accountID string) (negotiationclient.Proposal, error) {
				proposal, offerErr := negotiationService.ProposeAs(ctx, id, qty, accountID)
				return proposal, failure.Conversation(offerErr)
			},
			Counter: func(ctx context.Context, sessionID string, paise int64) (negotiationclient.Resolution, error) {
				resolution, counterErr := negotiationService.Counter(ctx, sessionID, paise)
				return resolution, failure.Conversation(counterErr)
			},
			Accept: func(ctx context.Context, sessionID string) (negotiationclient.Resolution, error) {
				resolution, acceptErr := negotiationService.Accept(ctx, sessionID)
				return resolution, failure.Conversation(acceptErr)
			},
			Decline: func(ctx context.Context, sessionID string, reason string) (negotiationclient.Resolution, error) {
				resolution, declineErr := negotiationService.Decline(ctx, sessionID, reason)
				return resolution, failure.Conversation(declineErr)
			},
		}
		// Reach the merchant as a native remote agent when its card is
		// published: the negotiating agent then delegates to a real agent instead
		// of only calling RPC tools.
		merchantAgent, merchantErr := remotemerchant.New(ctx, remotemerchant.Config{
			Endpoint:    negotiationEndpoint,
			SharedToken: os.Getenv("MARKET_SHARED_TOKEN"),
		})
		if merchantErr != nil {
			logger.Error("merchant remote agent unavailable", "error", merchantErr)
		}
		loopService, err = shopgraph.New(ctx, shopgraph.Config{
			APIKey:        buyerModel.APIKey,
			BaseURL:       buyerModel.BaseURL,
			Model:         buyerModel.Model,
			MerchantAgent: merchantAgent,
		}, loopTools)
		if err != nil {
			logger.Error("buyer agent loop configuration failed", "error", err)
			return
		}
	}
	// Publish the buyer as a discoverable agent when a token is configured.
	// Quote-only by design: the skill negotiates and returns terms, never debits.
	if token := strings.TrimSpace(os.Getenv("USER_AGENT_TOKEN")); token != "" && loopService != nil {
		addr := strings.TrimSpace(os.Getenv("USER_AGENT_ADDR"))
		if addr == "" {
			addr = ":8082"
		}
		cardURL := strings.TrimSpace(os.Getenv("USER_AGENT_CARD_URL"))
		if cardURL == "" {
			cardURL = "http://localhost" + addr + "/a2a/"
		}
		buyerHandler, handlerErr := newBuyerAgentHandler(shopperFunc(loopService.Run), cardURL, token)
		if handlerErr != nil {
			logger.Error("buyer agent service configuration failed", "error", handlerErr)
			return
		}
		buyerServer := &http.Server{Addr: addr, Handler: buyerHandler}
		go func() {
			if serveErr := buyerServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				logger.Error("buyer agent service stopped", "error", serveErr)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if shutErr := buyerServer.Shutdown(shutdownCtx); shutErr != nil {
				logger.Error("buyer agent service shutdown failed", "error", shutErr)
			}
		}()
		logger.Info("buyer agent published", "addr", addr, "card", cardURL+".well-known/agent-card.json")
	}
	// One command that says which layer is down, so a failed run never needs a
	// log dive to explain itself.
	layerReport := func(probeCtx context.Context) string {
		return health.Format(health.Run(probeCtx, []health.Probe{
			{Name: "records database", Layer: failure.LayerRecords, Check: func(ctx context.Context) error {
				_, probeErr := store.AccountForTelegram(ctx, 0)
				if probeErr != nil && strings.Contains(strings.ToLower(probeErr.Error()), "not found") {
					return nil
				}
				return probeErr
			}},
			{Name: "catalog tool channel", Layer: failure.LayerCatalog, Check: func(ctx context.Context) error {
				_, probeErr := catalogReader.Search(ctx, catalog.SearchRequest{})
				return probeErr
			}},
			{Name: "merchant conversation channel", Layer: failure.LayerConversation, Check: conversationProbe(negotiationEndpoint)},
			{Name: "reasoning layer", Layer: failure.LayerReasoning, Check: reasoningProbe()},
		}))
	}

	pollContext := ctx
	offset := 0
	var checkpoints telegramOffsetStore
	var conversations conversationMemory
	redisURL := strings.TrimSpace(os.Getenv("UPSTASH_REDIS_REST_URL"))
	redisToken := strings.TrimSpace(os.Getenv("UPSTASH_REDIS_REST_TOKEN"))
	if redisURL != "" && redisToken != "" {
		redisStore, storeErr := negotiation.NewRedisSessionStore(redisURL, redisToken, &http.Client{Timeout: 10 * time.Second})
		if storeErr != nil {
			logger.Error("telegram offset store configuration failed", "error", storeErr)
			return
		}
		checkpoints = redisOffsetStore{store: redisStore}
		// Without this the bot answers every message as if it were the first one.
		conversations = redisConversations{store: redisStore}
	} else {
		checkpoints = newOffsetStore(os.Getenv("TELEGRAM_OFFSET_FILE"))
		logger.Warn("conversation memory is unavailable, so each message is answered on its own")
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
			select {
			case <-pollContext.Done():
				return
			case <-time.After(pollBackoff):
			}
			continue
		}
		offset, err = processUpdates(pollContext, updates, offset, checkpoints, func(ctx context.Context, message *telegram.Message) error {
			err := handleMessage(ctx, client, linker, purchaseService, refundService, commandServices{negotiations: negotiationService, audit: store, accounts: store, catalog: catalogReader, approvals: approvalStore, conversations: conversations, loop: loopService, health: layerReport}, message)
			if err != nil {
				// The row is written before the person is told it exists. Telling them
				// a failure was recorded and then discarding the write is the one
				// thing this trail is not allowed to do: it claims evidence that is
				// not there.
				note := "\nRecorded for review."
				if recordErr := store.RecordUpdateDeadLetter(ctx, message.From.ID, message.Text, err); recordErr != nil {
					logger.Error("dead letter write failed", "error", recordErr, "cause", err)
					note = "\nThis could not be recorded, so please mention it if it happens again."
				}
				if sendErr := client.SendMessage(ctx, message.Chat.ID, failure.Explain(err)+note); sendErr != nil {
					logger.Error("failure reply failed", "error", sendErr, "cause", err)
				}
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

func (p loggingPurchaser) RequestApproval(ctx context.Context, request buyer.PurchaseRequest, reason string) (buyer.PurchaseResult, error) {
	result, err := p.inner.RequestApproval(ctx, request, reason)
	if err != nil {
		log.Printf("approval request error: %v", err)
	}
	return result, err
}

// The wrapper has this method whatever it wraps, so the caller's own check that
// the purchaser can resolve approvals always passes and the real question lands
// here. Answer it with an error: the bot says approval is unavailable and stays
// up, where an unchecked conversion would take the process down mid conversation.
func (p loggingPurchaser) ResolveApproval(ctx context.Context, telegramID int64, token, decision string) (buyer.PurchaseResult, error) {
	resolver, ok := p.inner.(approvalResolver)
	if !ok {
		return buyer.PurchaseResult{}, errors.New("this purchaser cannot resolve approvals")
	}
	result, err := resolver.ResolveApproval(ctx, telegramID, token, decision)
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

func (n loggingNegotiator) Browse(ctx context.Context, brief string, budgetPaise int64, accountID string) (negotiationclient.Shortlist, error) {
	result, err := n.inner.Browse(ctx, brief, budgetPaise, accountID)
	if err != nil {
		log.Printf("shop browse error: %v", err)
	}
	return result, err
}

func (n loggingNegotiator) Propose(ctx context.Context, productID string, quantity int) (negotiationclient.Proposal, error) {
	result, err := n.inner.Propose(ctx, productID, quantity)
	if err != nil {
		log.Printf("negotiation propose error: %v", err)
	}
	return result, err
}

func (n loggingNegotiator) ProposeAs(ctx context.Context, productID string, quantity int, accountID string) (negotiationclient.Proposal, error) {
	result, err := n.inner.ProposeAs(ctx, productID, quantity, accountID)
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
	// One message from the person is one run. Everything either side records
	// while answering it carries the same id, so the trail reads as one story.
	ctx = runid.With(ctx, runid.New())
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
		// The acknowledgement only clears the spinner on the tapped button. It
		// must never decide whether the person is told what happened to their
		// money, so a failure here is reported and stepped over.
		if err := client.AnswerCallbackQuery(ctx, message.CallbackQueryID); err != nil {
			log.Printf("acknowledging the tapped button failed: %v", err)
		}
	}
	return sendReply(ctx, client, message.Chat.ID, response, replyMarkupForResponse(response))
}

// conversationalBuy routes free-text requests ("buy me a trimmer under 2500")
// through the bounded agent loop, then executes the settled decision through
// the same Gate-guarded purchase path as every other command.
func conversationalBuy(ctx context.Context, client *telegram.Client, purchases purchaser, services commandServices, message *telegram.Message) error {
	if services.loop == nil || services.accounts == nil || services.catalog == nil || services.negotiations == nil {
		return sendReply(ctx, client, message.Chat.ID, "The shopping agent is not configured here. Use /start to see available commands.", nil)
	}
	// A decision the person has not answered outranks a new request. Starting a
	// fresh run here used to abandon the open question and quote them something
	// else, which reads as the agent forgetting what it just asked.
	//
	// What they typed is never read as consent. It can only put the same question
	// back in front of them, because interpreting words as approval would put text
	// interpretation in the charge path.
	if services.approvals != nil {
		pending, open, pendingErr := services.approvals.PendingFor(ctx, message.From.ID)
		if pendingErr != nil {
			// A failed read must not block shopping, so this falls through to the
			// behaviour that existed before the lookup did.
			log.Printf("open decision lookup failed: %v", pendingErr)
		} else if open {
			return sendReply(ctx, client, message.Chat.ID,
				openDecisionMessage(ctx, services.catalog, pending), approvalMarkup(pending.Token))
		}
	}
	account, accountErr := services.accounts.AccountForTelegram(ctx, message.From.ID)
	if accountErr != nil {
		log.Printf("agent loop account lookup failed: %v", accountErr)
		return sendReply(ctx, client, message.Chat.ID, "Link your account first: generate a token on the dashboard website, then send /link TOKEN.", nil)
	}
	working(ctx, client, message.Chat.ID)
	if err := sendReply(ctx, client, message.Chat.ID, fmt.Sprintf("Working on it: %q", strings.TrimSpace(message.Text)), nil); err != nil {
		return fmt.Errorf("send agent ack failed: %w", err)
	}
	// What this chat already discussed. A follow up such as "the second one" or
	// "cheaper" is answered against it, so the agents continue the conversation
	// rather than opening a new one. Memory that cannot be read is treated as no
	// memory, which is the behaviour that existed before it did.
	var prior shopgraph.Conversation
	if services.conversations != nil {
		remembered, memoryErr := services.conversations.Load(ctx, message.From.ID)
		if memoryErr != nil {
			log.Printf("conversation memory read failed: %v", memoryErr)
		} else {
			prior = remembered
		}
	}
	notes := &liveNotes{client: client, chatID: message.Chat.ID}
	result, runErr := services.loop.ContinueWithProgress(ctx, message.Text, prior, shopgraph.Wallet{
		BalancePaise:    account.WalletBalancePaise,
		SpendLimitPaise: account.SpendLimitPaise,
		AccountID:       account.ID,
	}, func(line string) {
		// The person watches the conversation happen in one message that grows, so
		// a stalled run still shows which step it stalled on without burying the
		// answer under a bubble per step.
		notes.add(ctx, line)
	})
	// The shortlist outlives the reply, so the next message can refer to it. The
	// original brief is kept rather than overwritten, because a refinement narrows
	// what was asked for instead of replacing it.
	//
	// Recorded here, before the paths that end early, because the graph attaches
	// what the shop showed even to a run that failed. A run that broke after the
	// shortlist is exactly when a person says "try again" or "the second one", and
	// recording this after the failure check threw that away.
	remember(ctx, services, message.From.ID, shopgraph.Conversation{
		Brief:   firstNonBlank(prior.Brief, message.Text),
		Options: result.Shown,
		Chosen:  result.ProductID,
	})
	if runErr != nil {
		// Strict mode: the human sees the real failure instead of a silent
		// scripted purchase.
		return sendReply(ctx, client, message.Chat.ID, failure.Explain(runErr), nil)
	}
	// Explainability: persist the graph's decision and node trace before any
	// money moves, so the dashboard can justify the purchase afterwards.
	if services.audit != nil {
		if auditErr := services.audit.RecordAgentRun(ctx, message.From.ID, message.Text, buyer.AgentRun{
			Action: string(result.Action), ProductID: result.ProductID, ProductName: result.ProductName,
			Quantity: result.Quantity, FinalPaise: result.FinalPaise, SessionID: result.SessionID,
			Accepted: result.Accepted, NeedsHuman: result.NeedsApproval,
			Rationale: result.Rationale, Steps: result.Steps,
			Transcript: result.Transcript,
		}); auditErr != nil {
			return fmt.Errorf("audit agent run: %w", auditErr)
		}
	}
	// The conversation is the evidence, so it is sent once, whatever the outcome.
	if len(result.Transcript) > 0 {
		document := renderTranscript(transcriptHeader{
			Request:   message.Text,
			Product:   result.ProductName,
			Outcome:   string(result.Action),
			Amount:    result.FinalPaise,
			SessionID: result.SessionID,
		}, result.Transcript)
		if docErr := client.SendDocument(ctx, message.Chat.ID, transcriptFileName(result.SessionID), document); docErr != nil {
			log.Printf("send negotiation transcript failed: %v", docErr)
		}
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "Agent decision: %s\n%s", result.Action, result.Rationale)
	for i, step := range result.Steps {
		if i >= len(result.Steps)-3 && i < len(result.Steps) {
			fmt.Fprintf(&summary, "\n- %s", step)
		}
	}
	if result.Action == shopgraph.ActionDecline {
		return sendReply(ctx, client, message.Chat.ID, summary.String(), nil)
	}
	product, perr := services.catalog.Get(ctx, result.ProductID)
	if perr != nil {
		return sendReply(ctx, client, message.Chat.ID, "The selected product is no longer available.", nil)
	}
	baseAmount := product.PricePaise * int64(result.Quantity)
	purchaseRequest := buyer.PurchaseRequest{
		TelegramID:       message.From.ID,
		ProductID:        result.ProductID,
		Quantity:         result.Quantity,
		BaseAmountPaise:  baseAmount,
		FinalAmountPaise: result.FinalPaise,
		IdempotencyKey:   fmt.Sprintf("telegram:nl:%d:%d", message.From.ID, message.MessageID),
		PriceObservedAt:  result.QuotedAt,
	}

	// The buyer agent itself asked for the human: record a pending approval and
	// hand the decision over instead of spending.
	if result.Action == shopgraph.ActionAskHuman {
		pending, approvalErr := purchases.RequestApproval(ctx, purchaseRequest, result.Rationale)
		if approvalErr != nil {
			return fmt.Errorf("request human approval: %w", approvalErr)
		}
		ask := fmt.Sprintf("Your call: %s for INR %.2f. Approval token: %s",
			product.Name, float64(pending.AmountPaise)/100, pending.ApprovalToken)
		ask += "\nTap Approve or Decline below, or send /approve " + pending.ApprovalToken
		ask += "\n\n" + summary.String()
		return sendReply(ctx, client, message.Chat.ID, ask, approvalMarkup(pending.ApprovalToken))
	}

	purchase, err := purchases.Purchase(ctx, purchaseRequest)
	if err != nil {
		return fmt.Errorf("agentic purchase failed: %w", err)
	}
	if purchase.ApprovalRequired {
		approval := fmt.Sprintf("Human approval required for INR %.2f. Approval token: %s", float64(purchase.AmountPaise)/100, purchase.ApprovalToken)
		approval += "\n\n" + summary.String()
		return sendReply(ctx, client, message.Chat.ID, approval, approvalMarkup(purchase.ApprovalToken))
	}
	if !purchase.Fulfilled {
		fmt.Fprintf(&summary, "\nPurchase rejected: %s", purchase.Reason)
		return sendReply(ctx, client, message.Chat.ID, summary.String(), nil)
	}
	fmt.Fprintf(&summary, "\nPurchase fulfilled via wallet for INR %.2f. Order: %s", float64(purchase.AmountPaise)/100, purchase.OrderID)
	forget(ctx, services, message.From.ID)
	return sendReply(ctx, client, message.Chat.ID, summary.String(), cancelMarkup(purchase.OrderID))
}

// remember records what this chat discussed. A failed write costs the next
// message its context and nothing else, so it is reported rather than returned.
func remember(ctx context.Context, services commandServices, telegramID int64, prior shopgraph.Conversation) {
	if services.conversations == nil {
		return
	}
	if err := services.conversations.Save(ctx, telegramID, prior); err != nil {
		log.Printf("conversation memory write failed: %v", err)
	}
}

// forget drops what this chat discussed, which is what a settled purchase means:
// the shortlist has been bought, so it is not something to refine any more and
// the next request should start from the person's own words rather than inherit a
// decided conversation. Every path that moves money ends here, including the ones
// that resume a purchase a person had to approve first.
func forget(ctx context.Context, services commandServices, telegramID int64) {
	remember(ctx, services, telegramID, shopgraph.Conversation{})
}

// firstNonBlank returns the first value that is not blank, or "".
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

// transcriptHeader is what the conversation was about, so the file explains
// itself when it is opened away from the chat it came from.
type transcriptHeader struct {
	Request   string
	Product   string
	Outcome   string
	Amount    int64
	SessionID string
}

// speakerName gives each side the name a reader would use rather than the name
// the rows are stored under.
func speakerName(actor string) string {
	switch strings.ToLower(strings.TrimSpace(actor)) {
	case "buyer":
		return "Shopper"
	case "merchant":
		return "Shop"
	default:
		return actor
	}
}

// renderTranscript writes the conversation as evidence: what was asked, what was
// said in order, and what it settled at. The turns are recorded elsewhere and only
// read here, so nothing in this file can change what happened.
func renderTranscript(header transcriptHeader, turns []negotiation.Turn) string {
	var builder strings.Builder
	builder.WriteString("AgentMart negotiation transcript\n")
	builder.WriteString(strings.Repeat("=", 33))
	builder.WriteString("\n\n")
	if request := strings.TrimSpace(header.Request); request != "" {
		fmt.Fprintf(&builder, "Asked for:  %s\n", request)
	}
	if product := strings.TrimSpace(header.Product); product != "" {
		fmt.Fprintf(&builder, "Product:    %s\n", product)
	}
	if outcome := strings.TrimSpace(header.Outcome); outcome != "" {
		fmt.Fprintf(&builder, "Outcome:    %s\n", outcome)
	}
	if header.Amount > 0 {
		fmt.Fprintf(&builder, "Settled at: INR %.2f\n", float64(header.Amount)/100)
	}
	if session := strings.TrimSpace(header.SessionID); session != "" {
		fmt.Fprintf(&builder, "Session:    %s\n", session)
	}
	if len(turns) > 0 {
		fmt.Fprintf(&builder, "Turns:      %d\n", len(turns))
	}
	builder.WriteString("\nConversation\n")
	builder.WriteString(strings.Repeat("-", 33))
	builder.WriteString("\n\n")
	if len(turns) == 0 {
		builder.WriteString("No turns were recorded for this run.\n")
		return builder.String()
	}
	for _, turn := range turns {
		fmt.Fprintf(&builder, "[%s] %s\n", turn.At.Format("15:04:05"), speakerName(turn.Actor))
		fmt.Fprintf(&builder, "    %s\n\n", strings.TrimSpace(turn.Message))
	}
	return builder.String()
}

// openDecisionMessage restates the decision the person was asked for and has not
// answered. It never interprets what they typed: a typed message cannot approve a
// spend, so the only thing this does is put the same question in front of them
// again, with the same token and the same two buttons.
func openDecisionMessage(ctx context.Context, products productFactsReader, pending buyer.PendingApproval) string {
	name := "the item you were shown"
	if products != nil {
		if product, err := products.Get(ctx, pending.ProductID); err == nil && strings.TrimSpace(product.Name) != "" {
			name = product.Name
		}
	}
	var open strings.Builder
	fmt.Fprintf(&open, "You still have a decision open: %s for INR %.2f.", name, float64(pending.FinalAmountPaise)/100)
	if reason := strings.TrimSpace(pending.Reason); reason != "" {
		fmt.Fprintf(&open, "\nWhy it needs you: %s", reason)
	}
	fmt.Fprintf(&open, "\n\nTap Approve or Decline below, or send /approve %s", pending.Token)
	open.WriteString("\nNothing has moved. A typed message cannot approve a spend.")
	return open.String()
}

// approvalMarkup puts the decision one tap away. The token is passed in rather
// than read back out of the message text, so a reworded prompt cannot silently
// leave a person holding a token and no way to answer.
func approvalMarkup(token string) *telegram.InlineKeyboardMarkup {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "Approve", CallbackData: "/approve " + token},
		{Text: "Decline", CallbackData: "/reject " + token},
	}}}
}

// cancelMarkup puts cancellation one tap away. It takes the order id the
// refund path is keyed on, never a payment reference and never text scraped
// back out of a message.
func cancelMarkup(orderID string) *telegram.InlineKeyboardMarkup {
	if strings.TrimSpace(orderID) == "" {
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "Cancel Order", CallbackData: "/refund " + orderID + " Cancelled by user"},
	}}}
}

// replyMarkupForResponse decides which buttons a reply carries. It reads the
// message this file just composed, so the two must be changed together: the test
// beside it drives the real command paths and fails if a rewording ever leaves a
// person holding a token with no way to answer it.
func replyMarkupForResponse(response string) *telegram.InlineKeyboardMarkup {
	if marker := "Approval token: "; strings.Contains(response, marker) {
		token := response[strings.Index(response, marker)+len(marker):]
		return approvalMarkup(strings.TrimSpace(strings.SplitN(token, "\n", 2)[0]))
	}
	// An order that has just been sent back is not one to offer sending back
	// again. The duplicate guard would refuse the second attempt, but offering it
	// at all reads as the system not knowing what it just did.
	if strings.Contains(response, "Refund") {
		return nil
	}
	if marker := "Order: "; strings.Contains(response, marker) {
		order := response[strings.Index(response, marker)+len(marker):]
		return cancelMarkup(strings.TrimSpace(strings.SplitN(order, " ", 2)[0]))
	}
	return nil
}

// conversationProbe checks the merchant answers on its conversation address and
// accepts our shared token, without spending a model call.
func conversationProbe(endpoint string) func(context.Context) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	return func(ctx context.Context) error {
		client, err := marketauth.NewClient(os.Getenv("MARKET_SHARED_TOKEN"), &http.Client{Timeout: 15 * time.Second})
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode >= http.StatusBadRequest {
			return fmt.Errorf("merchant answered %s", response.Status)
		}
		return nil
	}
}

// reasoningProbe asks the model provider for one trivial structured answer. This
// is the layer that fails most often, so it is worth a real call.
func reasoningProbe() func(context.Context) error {
	model := modelconfig.FromEnv("USER")
	if model.Model == "" || model.APIKey == "" {
		return nil
	}
	return func(ctx context.Context) error {
		reasoner := llmchat.New(model.Model, model.APIKey, model.BaseURL)
		_, err := reasoner.CompleteJSON(ctx, llmchat.CompleteRequest{
			System:       "Answer with the word ready.",
			User:         `{"check":"are you reachable"}`,
			FunctionName: "report_ready",
			Description:  "Report that you can answer.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"status": map[string]any{"type": "string"}},
				"required":   []string{"status"},
			},
		})
		return err
	}
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
		return strings.Join([]string{
			"Welcome to AgentMart.",
			"",
			"Tell me what you want in your own words and I will shop for it:",
			"  buy me a trimmer under 2500",
			"  something cheaper",
			"  the second one",
			"",
			"I show you what the shop offers, argue the price, and buy it if it is",
			"inside the limits you set. Anything outside them comes back to you to",
			"approve or decline.",
			"",
			"If you prefer commands:",
			"  /link TOKEN to connect the account you made on the website",
			"  /buy PRODUCT_ID QUANTITY to buy a listed item outright",
			"  /negotiate PRODUCT_ID QUANTITY to ask for a price first",
			"  /refund ORDER_ID REASON to send an order back",
			"  /diag to check that every part of the system is answering",
		}, "\n"), nil
	case "/diag":
		if services.health == nil {
			return "Layer checks are not configured in this build.", nil
		}
		return services.health(ctx), nil
	case "/link":
		if len(command) != 2 {
			return "Use /link TOKEN after generating a token in the dashboard.", nil
		}
		if _, err := linker.Redeem(ctx, command[1], telegramID); err != nil {
			return "That link token is invalid, expired, or already used.", nil
		}
		return "Telegram is now linked to your AgentMart wallet.", nil
	case "/buy":
		// No open decision check here, deliberately, and the same for /accept. Free
		// text is blocked while a question is standing because starting a fresh run
		// would abandon that question and quote something else, and because words
		// must never be read as consent. Neither applies to a command that names its
		// own product and quantity: it is an unambiguous instruction, not something
		// to interpret, and it abandons nothing. Each escalation carries its own
		// token and its own buttons, so a second one does not hide the first, and a
		// resumed approval goes back through the gate, where the balance is checked
		// after the human step and before anything is spent. Asking for two things
		// at once cannot overspend the wallet.
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
		forget(ctx, services, telegramID)
		return fmt.Sprintf("Purchase fulfilled via wallet for INR %.2f. Order: %s", float64(result.AmountPaise)/100, result.OrderID), nil
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
		// The quote's age travels with it. Without this the gate was handed the clock
		// at purchase time, so the five minute freshness window could never fire on a
		// negotiated premium, and the shop's own session lifetime was the only bound
		// on how old an accepted price could be.
		result, err := purchases.Purchase(ctx, buyer.PurchaseRequest{TelegramID: telegramID, ProductID: resolution.ProductID, Quantity: resolution.Quantity, BaseAmountPaise: resolution.BaseAmountPaise, FinalAmountPaise: resolution.FinalAmountPaise, IdempotencyKey: fmt.Sprintf("telegram:negotiation:%d:%s", telegramID, resolution.SessionID), PriceObservedAt: resolution.QuotedAt})
		if err != nil {
			return "Negotiated purchase could not be completed.", nil
		}
		if result.ApprovalRequired {
			return fmt.Sprintf("Human approval required for INR %.2f. Approval token: %s", float64(result.AmountPaise)/100, result.ApprovalToken), nil
		}
		if !result.Fulfilled {
			return "Negotiated purchase rejected: " + result.Reason, nil
		}
		forget(ctx, services, telegramID)
		return fmt.Sprintf("Negotiated purchase fulfilled via wallet for INR %.2f. Order: %s", float64(result.AmountPaise)/100, result.OrderID), nil
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
		// The answer settled the purchase the shortlist was for. A rejection returns
		// above this line and keeps the conversation, because a person who declined
		// one ask may still want to refine it.
		forget(ctx, services, telegramID)
		return fmt.Sprintf("Purchase fulfilled via wallet for INR %.2f. Order: %s", float64(result.AmountPaise)/100, result.OrderID), nil
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
		return strings.Join([]string{
			"I did not recognise that.",
			"",
			"Plain sentences work best, for example: buy me a trimmer under 2500.",
			"Send /start to see everything I can do.",
		}, "\n"), nil
	}
}
