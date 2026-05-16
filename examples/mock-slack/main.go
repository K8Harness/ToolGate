package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	gatewayURL    string
	signingSecret string
)

func main() {
	gatewayURL = os.Getenv("GATEWAY_URL")
	signingSecret = os.Getenv("SLACK_SIGNING_SECRET")
	if gatewayURL == "" || signingSecret == "" {
		log.Fatal("GATEWAY_URL and SLACK_SIGNING_SECRET are required")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat.postMessage", handleChatPostMessage)
	log.Println("mock-slack listening on :8090")
	log.Fatal(http.ListenAndServe(":8090", mux))
}

// handleChatPostMessage receives a Block Kit chat.postMessage notification from the gateway,
// extracts the ticket_id, immediately returns {"ok":true}, then asynchronously sends an
// approve action back to the gateway's /slack/actions endpoint.
func handleChatPostMessage(w http.ResponseWriter, r *http.Request) {
	// Read body BEFORE writing any response.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("mock-slack: read body error: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ticketID, err := extractTicketID(body)
	if err != nil {
		log.Printf("mock-slack: extract ticket_id error: %v", err)
		// Still return ok so the gateway notifier doesn't error.
	}

	// Return {"ok":true} immediately.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`)) //nolint:errcheck

	if ticketID != "" {
		go sendApproveAction(ticketID)
	}
}

// extractTicketID traverses the Block Kit blocks array to find the "actions" block
// and returns the value of the first button element (which is the ticket_id).
func extractTicketID(body []byte) (string, error) {
	// Use a generic map so we can traverse the heterogeneous blocks array.
	var msg struct {
		Blocks []map[string]json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", err
	}

	for _, block := range msg.Blocks {
		var blockType string
		if raw, ok := block["type"]; ok {
			if err := json.Unmarshal(raw, &blockType); err != nil {
				continue
			}
		}
		if blockType != "actions" {
			continue
		}

		// Found the actions block — extract elements.
		var elements []map[string]json.RawMessage
		if raw, ok := block["elements"]; ok {
			if err := json.Unmarshal(raw, &elements); err != nil {
				return "", err
			}
		}
		if len(elements) == 0 {
			return "", nil
		}

		var value string
		if raw, ok := elements[0]["value"]; ok {
			if err := json.Unmarshal(raw, &value); err != nil {
				return "", err
			}
		}
		return value, nil
	}
	return "", nil
}

// signPayload computes the Slack HMAC-SHA256 signature over "v0:<timestamp>:<body>"
// using the shared signing secret and returns a "v0=<hex>" string.
func signPayload(secret, timestamp string, body []byte) string {
	base := "v0:" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// sendApproveAction waits 50 ms then POSTs a signed block_actions payload to
// the gateway's /slack/actions endpoint with action_id "approval_approve".
func sendApproveAction(ticketID string) {
	time.Sleep(50 * time.Millisecond)

	// Build the JSON payload.
	type action struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	}
	type user struct {
		ID string `json:"id"`
	}
	type payload struct {
		Type    string   `json:"type"`
		User    user     `json:"user"`
		Actions []action `json:"actions"`
	}
	p := payload{
		Type: "block_actions",
		User: user{ID: "mock-user"},
		Actions: []action{
			{ActionID: "approval_approve", Value: ticketID},
		},
	}
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		log.Printf("mock-slack: marshal approve payload error: %v", err)
		return
	}

	// URL-encode as form body: payload=<url_encoded_json>
	formBody := []byte("payload=" + url.QueryEscape(string(payloadJSON)))

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sig := signPayload(signingSecret, timestamp, formBody)

	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/slack/actions", strings.NewReader(string(formBody)))
	if err != nil {
		log.Printf("mock-slack: build approve request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("mock-slack: approve POST error: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("mock-slack: gateway /slack/actions response: %d", resp.StatusCode)
}
