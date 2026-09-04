// Tests for the Telegram webhook: the secret wall that stands in for the bearer
// token Telegram cannot send, the handover to the buyer's queue, and the URL we
// ask Telegram to post to.
package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/telegram"
)

const aDelivery = `{"update_id":7,"message":{"message_id":3,"chat":{"id":11},"from":{"id":11},"text":"buy me a trimmer"}}`

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func webhookRequest(secret, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, telegramWebhookPath, strings.NewReader(body))
	if secret != "" {
		request.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	return request
}

func TestWebhookRefusesToServeWithoutASecret(t *testing.T) {
	if _, err := newWebhookHandler("   ", make(chan telegram.Update, 1), quietLogger()); err == nil {
		t.Fatal("expected no secret to be refused: an open webhook lets anyone forge a message from a linked person")
	}
}

func TestWebhookRefusesADeliveryWithTheWrongSecret(t *testing.T) {
	deliveries := make(chan telegram.Update, 1)
	handler, err := newWebhookHandler("right", deliveries, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{"absent": "", "wrong": "nope", "longer": "right-plus"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, webhookRequest(secret, aDelivery))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s secret status = %d, want 401", name, recorder.Code)
		}
	}
	if len(deliveries) != 0 {
		t.Fatalf("a forged delivery reached the buyer: queued %d", len(deliveries))
	}
}

func TestWebhookHandsTheUpdateToTheBuyer(t *testing.T) {
	deliveries := make(chan telegram.Update, 1)
	handler, err := newWebhookHandler("right", deliveries, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, webhookRequest("right", aDelivery))
	if recorder.Code != http.StatusOK {
		t.Fatalf("accepted delivery status = %d, want 200", recorder.Code)
	}
	select {
	case update := <-deliveries:
		if update.UpdateID != 7 || update.Message == nil || update.Message.Text != "buy me a trimmer" || update.Message.Chat.ID != 11 {
			t.Fatalf("the update lost detail on the way through: %+v", update)
		}
	default:
		t.Fatal("the delivery was answered but never handed to the buyer")
	}
}

func TestWebhookRefusesAMalformedDelivery(t *testing.T) {
	deliveries := make(chan telegram.Update, 1)
	handler, err := newWebhookHandler("right", deliveries, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, webhookRequest("right", `{"update_id":`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed delivery status = %d, want 400", recorder.Code)
	}
	if len(deliveries) != 0 {
		t.Fatalf("a malformed delivery reached the buyer: queued %d", len(deliveries))
	}
}

// A full queue has to answer with a failure. Telegram retries a delivery it could
// not hand over, so 503 delays the message; a 200 would lose it in silence.
func TestWebhookAsksTelegramToRetryWhenTheQueueIsFull(t *testing.T) {
	deliveries := make(chan telegram.Update, 1)
	deliveries <- telegram.Update{UpdateID: 1}
	handler, err := newWebhookHandler("right", deliveries, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, webhookRequest("right", aDelivery))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("full queue status = %d, want 503 so telegram tries again", recorder.Code)
	}
}

// Which way in the buyer takes. A local run against a copy of the deployed
// environment has to be able to poll without editing the url out of its .env.
func TestWebhookTargetLetsPollingWin(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		url     string
		polling string
		want    string
	}{
		{name: "a url alone means webhook", url: "https://agents.example.com", want: "https://agents.example.com"},
		{name: "polling true beats a url", url: "https://agents.example.com", polling: "true", want: ""},
		{name: "polling 1 beats a url", url: "https://agents.example.com", polling: "1", want: ""},
		{name: "polling false keeps the webhook", url: "https://agents.example.com", polling: "false", want: "https://agents.example.com"},
		{name: "a value that is not a boolean is not one", url: "https://agents.example.com", polling: "maybe", want: "https://agents.example.com"},
		{name: "no url polls whatever polling says", polling: "false", want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := webhookTarget(testCase.url, testCase.polling); got != testCase.want {
				t.Fatalf("target = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestWebhookEndpointURLCompletesAndRefuses(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		given     string
		want      string
		wantError bool
	}{
		{name: "bare host gets the route appended", given: "https://agents.example.com", want: "https://agents.example.com" + telegramWebhookPath},
		{name: "trailing slash is not the site root", given: "https://agents.example.com/", want: "https://agents.example.com" + telegramWebhookPath},
		{name: "an explicit path is kept", given: "https://agents.example.com/hooks/tg", want: "https://agents.example.com/hooks/tg"},
		{name: "surrounding space is ignored", given: "  https://agents.example.com  ", want: "https://agents.example.com" + telegramWebhookPath},
		{name: "a query is dropped", given: "https://agents.example.com/hooks/tg?token=leak", want: "https://agents.example.com/hooks/tg"},
		{name: "plain http is refused", given: "http://agents.example.com", wantError: true},
		{name: "a host on its own is refused", given: "agents.example.com", wantError: true},
		{name: "an empty url is refused", given: "", wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := webhookEndpointURL(testCase.given)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("expected %q to be refused, got %q", testCase.given, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("endpoint = %q, want %q", got, testCase.want)
			}
		})
	}
}

// The webhook cannot sit behind the bearer wall, because Telegram has no way to
// present our token. This pins that it is reachable with its own secret alone,
// while the agent's routes still are not.
func TestWebhookIsServedOutsideTheBearerWall(t *testing.T) {
	deliveries := make(chan telegram.Update, 1)
	webhook, err := newWebhookHandler("right", deliveries, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newBuyerAgentHandler(&stubShopper{}, "http://localhost:8082/a2a/", "secret", webhook)
	if err != nil {
		t.Fatal(err)
	}

	delivered := httptest.NewRecorder()
	handler.ServeHTTP(delivered, webhookRequest("right", aDelivery))
	if delivered.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200 without any bearer token", delivered.Code)
	}

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("agent card status = %d, want 401: the webhook must not open the agent", anonymous.Code)
	}
}

// A deployment may have a bot and no published agent. The webhook still has to be
// served, and the shopper must not be reachable at all in that case.
func TestWebhookServesWithNoAgentPublished(t *testing.T) {
	deliveries := make(chan telegram.Update, 1)
	webhook, err := newWebhookHandler("right", deliveries, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newBuyerAgentHandler(nil, "", "", webhook)
	if err != nil {
		t.Fatal(err)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	delivered := httptest.NewRecorder()
	handler.ServeHTTP(delivered, webhookRequest("right", aDelivery))
	if delivered.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200", delivered.Code)
	}

	agent := httptest.NewRecorder()
	handler.ServeHTTP(agent, httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil))
	if agent.Code != http.StatusNotFound {
		t.Fatalf("agent card status = %d, want 404: an unpublished shopper must not exist", agent.Code)
	}
}
