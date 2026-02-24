package service

import (
	"encoding/json"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/sideshow/apns2/payload"
	"github.com/vultisig/notification/models"
)

func TestAPNPayload(t *testing.T) {
	request := models.NotificationRequest{
		VaultId:    "dasdasdfadf",
		VaultName:  "Second-test",
		QRCodeData: "helloworld",
	}
	p := payload.NewPayload().Alert(nil).
		AlertTitle("Vultisig Keysign request").
		AlertSubtitle("Vault: " + request.VaultName).
		AlertBody(request.QRCodeData).
		Sound("default").
		Badge(1)
	buf, err := json.Marshal(p)
	if err != nil {
		t.Error(err)
	}
	t.Log(string(buf))
}

func TestWebPushPayload(t *testing.T) {
	request := models.NotificationRequest{
		VaultId:    "test-vault-id",
		VaultName:  "MyVault",
		QRCodeData: "qr-data-here",
	}

	payloadData := map[string]string{
		"title":    "Vultisig Keysign request",
		"subtitle": "Vault: " + request.VaultName,
		"body":     request.QRCodeData,
	}
	buf, err := json.Marshal(payloadData)
	if err != nil {
		t.Fatalf("failed to marshal web push payload: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(buf, &result); err != nil {
		t.Fatalf("failed to unmarshal web push payload: %v", err)
	}

	if result["title"] != "Vultisig Keysign request" {
		t.Errorf("expected title 'Vultisig Keysign request', got '%s'", result["title"])
	}
	if result["subtitle"] != "Vault: MyVault" {
		t.Errorf("expected subtitle 'Vault: MyVault', got '%s'", result["subtitle"])
	}
	if result["body"] != "qr-data-here" {
		t.Errorf("expected body 'qr-data-here', got '%s'", result["body"])
	}
}

func TestWebPushSubscriptionUnmarshal(t *testing.T) {
	subscriptionJSON := `{"endpoint":"https://fcm.googleapis.com/fcm/send/example","keys":{"p256dh":"BNcRd...","auth":"tBHI..."}}`

	var subscription webpush.Subscription
	if err := json.Unmarshal([]byte(subscriptionJSON), &subscription); err != nil {
		t.Fatalf("failed to unmarshal subscription: %v", err)
	}

	if subscription.Endpoint != "https://fcm.googleapis.com/fcm/send/example" {
		t.Errorf("expected endpoint 'https://fcm.googleapis.com/fcm/send/example', got '%s'", subscription.Endpoint)
	}
	if subscription.Keys.P256dh != "BNcRd..." {
		t.Errorf("expected p256dh 'BNcRd...', got '%s'", subscription.Keys.P256dh)
	}
	if subscription.Keys.Auth != "tBHI..." {
		t.Errorf("expected auth 'tBHI...', got '%s'", subscription.Keys.Auth)
	}
}
