package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const UpstreamBudgetManagedKey = "upstream_budget_managed_channel"

type UpstreamBudgetAttempt struct {
	id   string
	db   *gorm.DB
	once sync.Once
}

func (a *UpstreamBudgetAttempt) Finish() {
	if a == nil {
		return
	}
	a.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Failed settlement leaves durable held liability; never free on error.
		_ = model.ChargeUpstreamCeiling(ctx, a.db, a.id)
	})
}

type upstreamBudgetBody struct {
	io.ReadCloser
	attempt *UpstreamBudgetAttempt
}

func (b *upstreamBudgetBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.attempt.Finish()
	}
	return n, err
}
func (b *upstreamBudgetBody) Close() error {
	err := b.ReadCloser.Close()
	b.attempt.Finish()
	return err
}

func (a *UpstreamBudgetAttempt) BindResponse(resp *http.Response) {
	if a == nil {
		return
	}
	if resp == nil || resp.Body == nil {
		a.Finish()
		return
	}
	resp.Body = &upstreamBudgetBody{ReadCloser: resp.Body, attempt: a}
}

// BeginUpstreamBudget is called on each FINAL outbound attempt, after mapping
// and parameter overrides. Only the existing managed OpenAI text pilot opts in.
// A declared ceiling is financial configuration, never an observed invoice.
func BeginUpstreamBudget(c *gin.Context, req *http.Request, channelID, channelType int) (*UpstreamBudgetAttempt, error) {
	if !c.GetBool(UpstreamBudgetManagedKey) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	var channel model.Channel
	if model.DB == nil || model.DB.WithContext(ctx).Select("id", "status", "upstream_budget_enabled").First(&channel, channelID).Error != nil {
		return nil, model.ErrUpstreamBudget
	}
	if !channel.UpstreamBudgetEnabled {
		return nil, nil
	}
	if channel.Status != common.ChannelStatusEnabled {
		return nil, model.ErrUpstreamBudget
	}
	var p model.UpstreamBudget
	if model.DB.WithContext(ctx).First(&p, "channel_id = ?", channelID).Error != nil || !p.Enabled || !p.CeilingConfirmed || len(p.CostBasisHash) != 64 || p.ValidUntil <= time.Now().Unix() || p.MaxRequestBytes < 1 || p.MaxRequestBytes > 1<<20 || p.MaxOutputTokens < 1 || p.MaxOutputTokens > 32768 {
		return nil, model.ErrUpstreamBudget
	}
	if common.DebugEnabled || common.TLSInsecureSkipVerify || channelType != constant.ChannelTypeOpenAI || req.Method != "POST" || c.Request.URL.Path != "/v1/chat/completions" || req.URL.Scheme != "https" || req.URL.Host != "api.openai.com" || req.URL.User != nil || req.URL.EscapedPath() != "/v1/chat/completions" || req.URL.RawQuery != "" || req.URL.Fragment != "" || req.Body == nil {
		return nil, model.ErrUpstreamBudget
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, p.MaxRequestBytes+1))
	_ = req.Body.Close()
	if err != nil || int64(len(data)) > p.MaxRequestBytes {
		return nil, model.ErrUpstreamBudget
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil {
		return nil, model.ErrUpstreamBudget
	}
	for key := range fields {
		switch key {
		case "model", "messages", "max_tokens", "max_completion_tokens", "n", "stream", "stream_options":
		default:
			return nil, model.ErrUpstreamBudget
		}
	}
	var name string
	if common.Unmarshal(fields["model"], &name) != nil || name != p.Model {
		return nil, model.ErrUpstreamBudget
	}
	var messages []map[string]json.RawMessage
	if common.Unmarshal(fields["messages"], &messages) != nil || len(messages) < 1 || len(messages) > 16 {
		return nil, model.ErrUpstreamBudget
	}
	for _, message := range messages {
		if len(message) != 2 {
			return nil, model.ErrUpstreamBudget
		}
		var role, content string
		if common.Unmarshal(message["role"], &role) != nil || common.Unmarshal(message["content"], &content) != nil {
			return nil, model.ErrUpstreamBudget
		}
		if string(message["content"]) == "null" {
			return nil, model.ErrUpstreamBudget
		}
		if role != "user" && role != "system" && role != "assistant" {
			return nil, model.ErrUpstreamBudget
		}
	}
	fields["messages"], err = common.Marshal(messages)
	if err != nil {
		return nil, model.ErrUpstreamBudget
	}
	var maximum int64
	limit, exists := fields["max_tokens"]
	if other, ok := fields["max_completion_tokens"]; ok {
		if exists {
			return nil, model.ErrUpstreamBudget
		}
		limit, exists = other, true
	}
	if !exists || common.Unmarshal(limit, &maximum) != nil || maximum < 1 || maximum > p.MaxOutputTokens {
		return nil, model.ErrUpstreamBudget
	}
	if n, ok := fields["n"]; ok {
		var count int64
		if common.Unmarshal(n, &count) != nil || count != 1 {
			return nil, model.ErrUpstreamBudget
		}
	}
	if stream, ok := fields["stream"]; ok {
		var value bool
		if string(stream) == "null" || common.Unmarshal(stream, &value) != nil {
			return nil, model.ErrUpstreamBudget
		}
	}
	if options, ok := fields["stream_options"]; ok {
		var value map[string]bool
		if common.Unmarshal(options, &value) != nil {
			return nil, model.ErrUpstreamBudget
		}
		for key := range value {
			if key != "include_usage" {
				return nil, model.ErrUpstreamBudget
			}
		}
	}
	// Canonicalize duplicates before sending; upstream parser precedence cannot
	// reinterpret a different output limit. Disable replayable-body retries.
	data, err = common.Marshal(fields)
	if err != nil || int64(len(data)) > p.MaxRequestBytes {
		return nil, model.ErrUpstreamBudget
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.GetBody = nil
	req.ContentLength = int64(len(data))
	if c.Request.Context().Err() != nil {
		return nil, model.ErrUpstreamBudget
	}
	r, err := model.ReserveUpstreamCost(ctx, model.DB, p)
	if err != nil {
		return nil, model.ErrUpstreamBudget
	}
	c.Header("X-Upstream-Reservation-ID", r.ID)
	return &UpstreamBudgetAttempt{id: r.ID, db: model.DB}, nil
}
