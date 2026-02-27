package models

type WSNotification struct {
	Type       string `json:"type"`        // always "notification"
	ID         string `json:"id"`          // Redis stream message ID
	VaultName  string `json:"vault_name"`
	QRCodeData string `json:"qr_code_data"`
}

type WSAck struct {
	Type string `json:"type"` // always "ack"
	ID   string `json:"id"`  // stream message ID being acknowledged
}
