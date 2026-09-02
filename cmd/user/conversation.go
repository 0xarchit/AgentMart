// Per chat conversation memory, so a follow up continues instead of restarting.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agentmart/internal/negotiation"
	"agentmart/internal/shopgraph"
)

// conversationTTL is how long a shortlist stays referable. Long enough that a
// person can go away and think about it, short enough that "the second one"
// cannot mean something they were shown yesterday at a price that has moved
// since.
const conversationTTL = 2 * time.Hour

// conversationMemory remembers what a chat has already discussed. It holds no
// money facts: the wallet, the limits and the amount are read fresh on every
// run, so nothing remembered here can widen a bound.
type conversationMemory interface {
	Load(ctx context.Context, telegramID int64) (shopgraph.Conversation, error)
	Save(ctx context.Context, telegramID int64, prior shopgraph.Conversation) error
}

// redisConversations keeps conversation state beside the poll checkpoint, so a
// restart does not lose what the shop just showed.
type redisConversations struct {
	store *negotiation.RedisSessionStore
}

// conversationKey scopes memory to one person.
func conversationKey(telegramID int64) string {
	return fmt.Sprintf("agentmart:chat:%d", telegramID)
}

// Load reads what this chat already discussed. Unreadable memory is treated as
// no memory, because a person asking for a trimmer should get an answer rather
// than a report about storage.
func (c redisConversations) Load(ctx context.Context, telegramID int64) (shopgraph.Conversation, error) {
	value, ok, err := c.store.GetValue(ctx, conversationKey(telegramID))
	if err != nil || !ok {
		return shopgraph.Conversation{}, err
	}
	var prior shopgraph.Conversation
	if json.Unmarshal([]byte(value), &prior) != nil {
		return shopgraph.Conversation{}, nil
	}
	return prior, nil
}

// Save records the shortlist and the brief behind it. Saving an empty
// conversation is how a finished purchase is forgotten, so the next request
// starts clean rather than inheriting a shortlist that has already been bought.
func (c redisConversations) Save(ctx context.Context, telegramID int64, prior shopgraph.Conversation) error {
	encoded, err := json.Marshal(prior)
	if err != nil {
		return fmt.Errorf("encode conversation memory: %w", err)
	}
	return c.store.PutValue(ctx, conversationKey(telegramID), string(encoded), conversationTTL)
}
