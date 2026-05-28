package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	larkAPIBaseURL       = "https://open.feishu.cn/open-apis"
	larkArgsTruncateAt   = 2000
	larkArgsTruncateMark = "... [truncated]"
)

// ApprovalNotifier abstracts outbound approval notification.
// Implemented by LarkClient; tests use a mock double.
type ApprovalNotifier interface {
	// SendApprovalRequest sends an interactive message with Approve/Deny buttons.
	// ticketID is embedded in button values for routing on callback.
	// Errors are non-fatal: the caller logs and continues the approval hold.
	SendApprovalRequest(ctx context.Context, ticketID string, t TicketRecord) error
}

// LarkClient sends interactive card approval request messages via Lark's messaging API.
type LarkClient struct {
	appID      string
	appSecret  string
	chatID     string
	httpClient *http.Client
	log        *slog.Logger
	apiBaseURL string // overridable for tests; defaults to larkAPIBaseURL
}

// NewLarkClient constructs a production-ready LarkClient.
func NewLarkClient(appID, appSecret, chatID, baseURL string, log *slog.Logger) *LarkClient {
	return newLarkClientWithHTTP(appID, appSecret, chatID, baseURL, &http.Client{Timeout: 15 * time.Second}, log)
}

// newLarkClientWithHTTP constructs a LarkClient with an injected HTTP client (used in tests).
func newLarkClientWithHTTP(appID, appSecret, chatID, baseURL string, httpClient *http.Client, log *slog.Logger) *LarkClient {
	if log == nil {
		log = slog.Default()
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if baseURL == "" {
		baseURL = larkAPIBaseURL
	}
	return &LarkClient{
		appID:      appID,
		appSecret:  appSecret,
		chatID:     chatID,
		httpClient: httpClient,
		log:        log,
		apiBaseURL: baseURL,
	}
}

// larkTenantTokenReq is the payload for the tenant access token endpoint.
type larkTenantTokenReq struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

// larkTenantTokenResp is the response from the tenant access token endpoint.
type larkTenantTokenResp struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
}

// larkCardText is a Lark card text element.
type larkCardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

// larkCardButton is a Lark interactive card button element.
type larkCardButton struct {
	Tag   string            `json:"tag"`
	Text  larkCardText      `json:"text"`
	Type  string            `json:"type"`
	Value map[string]string `json:"value"`
}

// larkCardAction is a Lark card action block containing buttons.
type larkCardAction struct {
	Tag     string           `json:"tag"`
	Actions []larkCardButton `json:"actions"`
}

// larkCardDiv is a Lark card markdown text block.
type larkCardDiv struct {
	Tag  string       `json:"tag"`
	Text larkCardText `json:"text"`
}

// larkCard is the top-level interactive card payload.
type larkCard struct {
	Config   map[string]bool `json:"config"`
	Elements []interface{}   `json:"elements"`
}

// larkSendMessageReq is the payload for Lark's im/v1/messages endpoint.
// Content is the JSON-encoded card string (Lark requires a JSON string, not object).
type larkSendMessageReq struct {
	ReceiveID string `json:"receive_id"`
	MsgType   string `json:"msg_type"`
	Content   string `json:"content"`
}

// fetchTenantToken obtains a short-lived tenant access token using app credentials.
func (c *LarkClient) fetchTenantToken(ctx context.Context) (string, error) {
	body, err := json.Marshal(larkTenantTokenReq{AppID: c.appID, AppSecret: c.appSecret})
	if err != nil {
		return "", fmt.Errorf("lark notifier: marshal token request: %w", err)
	}

	url := c.apiBaseURL + "/auth/v3/tenant_access_token/internal"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("lark notifier: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("lark notifier: token http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lark notifier: token endpoint status %d", resp.StatusCode)
	}

	var tokenResp larkTenantTokenResp
	rawBody, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(rawBody, &tokenResp); err != nil {
		return "", fmt.Errorf("lark notifier: decode token response: %w", err)
	}
	if tokenResp.Code != 0 {
		return "", fmt.Errorf("lark notifier: token error code %d: %s", tokenResp.Code, tokenResp.Msg)
	}
	return tokenResp.TenantAccessToken, nil
}

// SendApprovalRequest sends an interactive Lark card with Approve/Deny buttons.
// The ticketID and action are embedded in each button's value map so the webhook
// handler can route the decision back to the correct approval hold.
func (c *LarkClient) SendApprovalRequest(ctx context.Context, ticketID string, t TicketRecord) error {
	token, err := c.fetchTenantToken(ctx)
	if err != nil {
		return err
	}

	argsStr := truncateArgs(t.Arguments)
	cardText := fmt.Sprintf("**Tool:** %s\n**Arguments:** %s\n**Session:** %s",
		t.ToolName, argsStr, t.SessionID)

	card := larkCard{
		Config: map[string]bool{"wide_screen_mode": true},
		Elements: []interface{}{
			larkCardDiv{
				Tag:  "div",
				Text: larkCardText{Tag: "lark_md", Content: cardText},
			},
			larkCardAction{
				Tag: "action",
				Actions: []larkCardButton{
					{
						Tag:  "button",
						Text: larkCardText{Tag: "plain_text", Content: "Approve"},
						Type: "primary",
						Value: map[string]string{
							"ticket_id": ticketID,
							"action":    "approve",
						},
					},
					{
						Tag:  "button",
						Text: larkCardText{Tag: "plain_text", Content: "Deny"},
						Type: "danger",
						Value: map[string]string{
							"ticket_id": ticketID,
							"action":    "deny",
						},
					},
				},
			},
		},
	}

	cardJSON, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("lark notifier: marshal card: %w", err)
	}

	msgBody, err := json.Marshal(larkSendMessageReq{
		ReceiveID: c.chatID,
		MsgType:   "interactive",
		Content:   string(cardJSON),
	})
	if err != nil {
		return fmt.Errorf("lark notifier: marshal message request: %w", err)
	}

	url := c.apiBaseURL + "/im/v1/messages?receive_id_type=chat_id"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(msgBody))
	if err != nil {
		return fmt.Errorf("lark notifier: build message request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("lark notifier: message http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("lark notifier: unexpected status %d: %s", resp.StatusCode, body)
	}
	c.log.Info("lark approval card sent", "ticketID", ticketID, "chatID", c.chatID)
	return nil
}

// truncateArgs converts raw arguments JSON to a displayable string,
// truncating at larkArgsTruncateAt characters to stay within card limits.
func truncateArgs(args json.RawMessage) string {
	s := string(args)
	if len(s) > larkArgsTruncateAt {
		return s[:larkArgsTruncateAt] + larkArgsTruncateMark
	}
	return s
}
