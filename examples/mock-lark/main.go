package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

var (
	gatewayURL        string
	verificationToken string
)

func main() {
	gatewayURL = os.Getenv("GATEWAY_URL")
	verificationToken = os.Getenv("LARK_VERIFICATION_TOKEN")
	if gatewayURL == "" || verificationToken == "" {
		log.Fatal("GATEWAY_URL and LARK_VERIFICATION_TOKEN are required")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", handleTenantToken)
	mux.HandleFunc("/open-apis/im/v1/messages", handleSendMessage)
	log.Println("mock-lark listening on :8090")
	log.Fatal(http.ListenAndServe(":8090", mux))
}

// handleTenantToken returns a mock tenant access token — no real Lark credentials needed.
func handleTenantToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"mock-tenant-token","expire":7200}`))
}

// handleSendMessage receives a Lark interactive card message from the gateway,
// extracts the ticket_id, acknowledges immediately, then asynchronously sends
// an approve action back to the gateway's /lark/actions endpoint.
func handleSendMessage(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("mock-lark: read body error: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ticketID, err := extractTicketID(body)
	if err != nil {
		log.Printf("mock-lark: extract ticket_id error: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))

	if ticketID != "" {
		go sendApproveAction(ticketID)
	}
}

// extractTicketID parses the Lark card JSON (nested inside the content string field)
// to find the ticket_id value in the first button's value map.
func extractTicketID(msgBody []byte) (string, error) {
	var msg struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msgBody, &msg); err != nil {
		return "", fmt.Errorf("unmarshal message: %w", err)
	}

	var card struct {
		Elements []struct {
			Tag     string `json:"tag"`
			Actions []struct {
				Value map[string]string `json:"value"`
			} `json:"actions"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(msg.Content), &card); err != nil {
		return "", fmt.Errorf("unmarshal card: %w", err)
	}

	for _, elem := range card.Elements {
		if elem.Tag != "action" {
			continue
		}
		if len(elem.Actions) > 0 {
			return elem.Actions[0].Value["ticket_id"], nil
		}
	}
	return "", nil
}

// computeSignature computes the Lark-compatible HMAC signature used by the gateway.
// Formula: hex(sha256(verificationToken + timestamp + nonce + body))
func computeSignature(token, timestamp, nonce string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(token))
	h.Write([]byte(timestamp))
	h.Write([]byte(nonce))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// sendApproveAction waits 50ms then POSTs a signed Lark card callback payload to
// the gateway's /lark/actions endpoint with action "approve".
func sendApproveAction(ticketID string) {
	time.Sleep(50 * time.Millisecond)

	type actionValue struct {
		TicketID string `json:"ticket_id"`
		Action   string `json:"action"`
	}
	type cardAction struct {
		Tag   string      `json:"tag"`
		Value actionValue `json:"value"`
	}
	type callbackPayload struct {
		OpenID string     `json:"open_id"`
		Action cardAction `json:"action"`
	}

	payload := callbackPayload{
		OpenID: "ou_mock-user",
		Action: cardAction{
			Tag: "button",
			Value: actionValue{
				TicketID: ticketID,
				Action:   "approve",
			},
		},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		log.Printf("mock-lark: marshal approve payload error: %v", err)
		return
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "mock-nonce-" + timestamp
	sig := computeSignature(verificationToken, timestamp, nonce, payloadJSON)

	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/lark/actions", bytes.NewReader(payloadJSON))
	if err != nil {
		log.Printf("mock-lark: build approve request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lark-Request-Timestamp", timestamp)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("mock-lark: approve POST error: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	log.Printf("mock-lark: gateway /lark/actions response: %d", resp.StatusCode)
}
