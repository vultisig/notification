package models

import "fmt"

type NotificationRequest struct {
	VaultId      string `json:"vault_id" binding:"required"`
	VaultName    string `json:"vault_name,omitempty"`
	LocalPartyId string `json:"local_party_id,omitempty"`
	QRCodeData   string `json:"qr_code_data,omitempty"`
}

func (n *NotificationRequest) IsValid() error {
	if n.VaultId == "" {
		return fmt.Errorf("vault_id is empty")
	}
	if n.VaultName == "" {
		return fmt.Errorf("vault_name is empty")
	}
	if n.QRCodeData == "" {
		return fmt.Errorf("qr_code_data is empty")
	}
	if n.LocalPartyId == "" {
		return fmt.Errorf("local_party_id is empty")
	}
	return nil
}
