package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

const (
	slackAPIBaseURL       = "https://slack.com/api"
	slackArgsTruncateAt   = 2000
	slackArgsTruncateMark = "... [truncated]"
)

// SlackNotifier abstracts outbound approval notification.
// v0 implements with Slack chat.postMessage; v1+ may add other channels.
type SlackNotifier interface {
	// SendApprovalRequest sends a Block Kit message with Approve/Deny buttons.
	// ticketID is embedded in button values for routing on callback.
	// Errors are non-fatal: the caller logs and continues the approval hold.
	SendApprovalRequest(ctx context.Context, ticketID string, t TicketRecord) error
}

// SlackClient sends Block Kit approval request messages via Slack chat.postMessage.
type SlackClient struct {
	botToken   string
	channel    string
	httpClient *http.Client
	log        *slog.Logger
	apiBaseURL string // overridable for tests; defaults to slackAPIBaseURL
}

// NewSlackClient constructs a production-ready SlackClient.
func NewSlackClient(botToken, channel string, log *slog.Logger) *SlackClient {
	return newSlackClientWithHTTP(botToken, channel, &http.Client{}, log)
}

// newSlackClientWithHTTP constructs a SlackClient with an injected HTTP client.
// This is the internal constructor used by tests to inject a custom transport or
// redirect requests to a test server.
func newSlackClientWithHTTP(botToken, channel string, httpClient *http.Client, log *slog.Logger) *SlackClient {
	if log == nil {
		log = slog.Default()
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &SlackClient{
		botToken:   botToken,
		channel:    channel,
		httpClient: httpClient,
		log:        log,
		apiBaseURL: slackAPIBaseURL,
	}
}

// slackText is a Slack text object used inside blocks.
type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// slackSectionBlock is a Slack section block.
type slackSectionBlock struct {
	Type string    `json:"type"`
	Text slackText `json:"text"`
}

// slackButtonElement is a Slack button element inside an actions block.
type slackButtonElement struct {
	Type     string    `json:"type"`
	Text     slackText `json:"text"`
	ActionID string    `json:"action_id"`
	Value    string    `json:"value"`
}

// slackActionsBlock is a Slack actions block containing interactive elements.
type slackActionsBlock struct {
	Type     string               `json:"type"`
	Elements []slackButtonElement `json:"elements"`
}

// slackChatPostMessageRequest is the payload for Slack chat.postMessage.
type slackChatPostMessageRequest struct {
	Channel string        `json:"channel"`
	Blocks  []interface{} `json:"blocks"`
}

// SendApprovalRequest sends a Block Kit message to Slack containing the tool details
// and Approve/Deny action buttons. The ticketID is embedded in each button's value
// so the webhook handler can route the decision back to the correct approval hold.
func (c *SlackClient) SendApprovalRequest(ctx context.Context, ticketID string, t TicketRecord) error {
	argsStr := truncateArgs(t.Arguments)

	sectionText := fmt.Sprintf(
		"*Tool:* %s\n*Arguments:* %s\n*Session:* %s",
		t.ToolName,
		argsStr,
		t.SessionID,
	)

	payload := slackChatPostMessageRequest{
		Channel: c.channel,
		Blocks: []interface{}{
			slackSectionBlock{
				Type: "section",
				Text: slackText{
					Type: "mrkdwn",
					Text: sectionText,
				},
			},
			slackActionsBlock{
				Type: "actions",
				Elements: []slackButtonElement{
					{
						Type:     "button",
						Text:     slackText{Type: "plain_text", Text: "Approve"},
						ActionID: "approval_approve",
						Value:    ticketID,
					},
					{
						Type:     "button",
						Text:     slackText{Type: "plain_text", Text: "Deny"},
						ActionID: "approval_deny",
						Value:    ticketID,
					},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack notifier: marshal payload: %w", err)
	}

	url := c.apiBaseURL + "/chat.postMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack notifier: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack notifier: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack notifier: unexpected status %d", resp.StatusCode)
	}

	return nil
}

// truncateArgs converts the raw arguments JSON to a displayable string,
// truncating at slackArgsTruncateAt characters to stay within Slack's per-block limits.
func truncateArgs(args json.RawMessage) string {
	s := string(args)
	if len(s) > slackArgsTruncateAt {
		return s[:slackArgsTruncateAt] + slackArgsTruncateMark
	}
	return s
}
