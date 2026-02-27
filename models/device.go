package models

import (
	"fmt"

	"gorm.io/gorm"
)

type Device struct {
	VaultId       string `gorm:"type:varchar(255);not null;index:idx_vault_id" json:"vault_id" binding:"required"`
	PartyName     string `gorm:"type:varchar(255);not null" json:"party_name" binding:"required"`
	Token         string `gorm:"type:text;not null" json:"token" binding:"required"`
	DeviceType    string `gorm:"type:varchar(255);not null" json:"device_type" binding:"required"` // apple, android, or web
	AuthTokenHash string `gorm:"type:char(64);not null;uniqueIndex:idx_auth_token" json:"-"`
}

type DeviceDBModel struct {
	gorm.Model
	Device
}

func (*DeviceDBModel) TableName() string {
	return "devices"
}

func (d *Device) GetDeviceDBModel() DeviceDBModel {
	return DeviceDBModel{
		Device: Device{
			VaultId:       d.VaultId,
			PartyName:     d.PartyName,
			Token:         d.Token,
			DeviceType:    d.DeviceType,
			AuthTokenHash: d.AuthTokenHash,
		},
	}
}
func (d *Device) IsValid() error {
	if d.VaultId == "" {
		return fmt.Errorf("vault_id is empty")
	}
	if d.PartyName == "" {
		return fmt.Errorf("party_name is empty")
	}
	if d.Token == "" {
		return fmt.Errorf("token is empty")
	}
	return nil
}
