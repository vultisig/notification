package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vultisig/notification/config"
	"github.com/vultisig/notification/contexthelper"
	"github.com/vultisig/notification/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type Database struct {
	db *gorm.DB
}

func NewDatabase(cfg *config.DatabaseConfig) (*Database, error) {
	if nil == cfg {
		return nil, fmt.Errorf("config is nil")
	}
	database, err := gorm.Open(postgres.Open(cfg.DSN),
		&gorm.Config{
			Logger: logger.Default.LogMode(logger.Error),
		})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	err = database.AutoMigrate(&models.DeviceDBModel{})
	if err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return &Database{db: database}, nil
}

func (d *Database) RegisterDevice(ctx context.Context, device models.Device) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	newContext, cancel := contexthelper.GetNewTimeoutContext(ctx, 5*time.Second)
	defer func() {
		cancel()
	}()
	deviceModel := device.GetDeviceDBModel()
	result := d.db.WithContext(newContext).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "vault_id"}, {Name: "party_name"}, {Name: "token"}},
			DoUpdates: clause.AssignmentColumns([]string{"device_type", "updated_at"}),
		}).
		Create(&deviceModel)
	if result.Error != nil {
		return fmt.Errorf("failed to register device: %w", result.Error)
	}
	return nil
}
func (d *Database) UnregisterDevice(ctx context.Context, vaultId, tokenId string) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	if vaultId == "" {
		return fmt.Errorf("vaultId is empty")
	}
	if tokenId == "" {
		return fmt.Errorf("tokenId is empty")
	}
	newContext, cancel := contexthelper.GetNewTimeoutContext(ctx, 5*time.Second)
	defer func() {
		cancel()
	}()
	result := d.db.WithContext(newContext).Unscoped().Where("vault_id = ? and token = ?", vaultId, tokenId).Delete(&models.DeviceDBModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to unregister device: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("no device found with vaultId: %s", vaultId)
	}
	return nil
}
func (d *Database) UnregisterDeviceByParty(ctx context.Context, vaultId, partyName string) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	if vaultId == "" {
		return fmt.Errorf("vaultId is empty")
	}
	if partyName == "" {
		return fmt.Errorf("partyName is empty")
	}
	newContext, cancel := contexthelper.GetNewTimeoutContext(ctx, 5*time.Second)
	defer func() {
		cancel()
	}()
	result := d.db.WithContext(newContext).Unscoped().Where("vault_id = ? and party_name = ?", vaultId, partyName).Delete(&models.DeviceDBModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to unregister device: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("no device found with vaultId: %s and partyName: %s", vaultId, partyName)
	}
	return nil
}

func (d *Database) GetRegisteredDevices(ctx context.Context, vaultId string, requestPartyId string) ([]models.DeviceDBModel, error) {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	if vaultId == "" {
		return nil, fmt.Errorf("vaultId is empty")
	}
	newContext, cancel := contexthelper.GetNewTimeoutContext(ctx, 5*time.Second)
	defer func() {
		cancel()
	}()
	var devices []models.DeviceDBModel
	// don't send notification to itself
	dbResult := d.db.WithContext(newContext).Where("vault_id = ? and party_name != ?", vaultId, requestPartyId).Find(&devices)
	if dbResult.Error != nil {
		return nil, fmt.Errorf("failed to query devices: %w", dbResult.Error)
	}
	return devices, nil
}
func (d *Database) IsDeviceRegistered(ctx context.Context, vaultId string) (bool, error) {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return false, err
	}

	if vaultId == "" {
		return false, fmt.Errorf("vaultId is empty")
	}
	newContext, cancel := contexthelper.GetNewTimeoutContext(ctx, 5*time.Second)
	defer func() {
		cancel()
	}()
	dbResult := d.db.WithContext(newContext).Where("vault_id = ?", vaultId).First(&models.DeviceDBModel{})
	if dbResult.Error != nil {
		if errors.Is(dbResult.Error, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("failed to query device: %w", dbResult.Error)
	}
	return dbResult.RowsAffected > 0, nil
}

func (d *Database) Close() error {
	sqlDB, err := d.db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql.DB from gorm.DB: %w", err)
	}

	return sqlDB.Close()
}
