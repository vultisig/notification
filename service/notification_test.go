package service

import (
	"encoding/json"
	"testing"

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
