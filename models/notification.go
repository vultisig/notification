package models

import "fmt"

type NotificationRequest struct {
	VaultId string `json:"vault_id" binding:"required"`
	ImageId string `json:"image_id" binding:"required"`
}

func (n *NotificationRequest) IsValid() error {
	if n.VaultId == "" {
		return fmt.Errorf("vault_id is empty")
	}
	if n.ImageId == "" {
		return fmt.Errorf("image_id is empty")
	}
	return nil
}
