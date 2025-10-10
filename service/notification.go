package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/hibiken/asynq"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sirupsen/logrus"
	"github.com/vultisig/notification/contexthelper"
	"github.com/vultisig/notification/models"
	"github.com/vultisig/notification/storage"
)

const appID = "com.vultisig.wallet"

type NotificationService struct {
	logger      *logrus.Logger
	sdClient    *statsd.Client
	db          *storage.Database
	imageServer string
	certificate string
	password    string
}

func NewNotificationService(sdClient *statsd.Client,
	db *storage.Database, imageServer, certificate, password string) (*NotificationService, error) {
	if sdClient == nil {
		return nil, fmt.Errorf("sdClient is nil")
	}
	if db == nil {
		return nil, fmt.Errorf("db is nil")
	}
	return &NotificationService{
		logger:      logrus.WithField("service", "notification").Logger,
		sdClient:    sdClient,
		db:          db,
		imageServer: imageServer,
		certificate: certificate,
		password:    password,
	}, nil
}
func (s *NotificationService) incCounter(name string, tags []string) {
	if err := s.sdClient.Count(name, 1, tags, 1); err != nil {
		s.logger.Errorf("fail to count metric, err: %v", err)
	}
}
func (s *NotificationService) measureTime(name string, start time.Time, tags []string) {
	if err := s.sdClient.Timing(name, time.Since(start), tags, 1); err != nil {
		s.logger.Errorf("fail to measure time metric, err: %v", err)
	}
}

// HandleNotification processes a notification task.
func (s *NotificationService) HandleNotification(ctx context.Context, task *asynq.Task) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	var request models.NotificationRequest
	if err := json.Unmarshal(task.Payload(), &request); err != nil {
		s.logger.Errorf("failed to unmarshal task payload: %v", err)
		s.incCounter("notification.unmarshal_failure", []string{})
		return fmt.Errorf("failed to unmarshal task payload: %s, %w", err, asynq.SkipRetry)
	}
	if err := request.IsValid(); err != nil {
		s.logger.Errorf("invalid notification request: %v", err)
		return fmt.Errorf("invalid notification request: %s,%w", err, asynq.SkipRetry)
	}

	s.logger.Infof("Processing notification for user: %s, message: %s", request.VaultId, request.ImageId)

	return nil
}

func (s *NotificationService) ProcessNotificationRequest(ctx context.Context, request models.NotificationRequest) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	deviceRegistration, err := s.db.GetRegisteredDevices(ctx, request.VaultId)
	if err != nil {
		s.logger.Errorf("failed to get registered devices: %v", err)
		return fmt.Errorf("failed to get registered devices: %w", err)
	}
	if len(deviceRegistration) == 0 {
		s.logger.Infof("No registered devices found for vaultId: %s", request.VaultId)
		return nil
	}

	for _, device := range deviceRegistration {
		s.logger.Infof("Found registered device: %v", device)
		if strings.EqualFold(device.DeviceType, "apple") {
			if err := s.processAppleNotification(ctx, device, request); err != nil {
				s.logger.Errorf("failed to process apple notification: %v", err)
			}
		}
		if strings.EqualFold(device.DeviceType, "android") {
			if err := s.processAndroidNotification(ctx, device, request); err != nil {
				s.logger.Errorf("failed to process android notification: %v", err)
			}
		}
	}
	return nil
}

func (s *NotificationService) processAppleNotification(ctx context.Context, device models.DeviceDBModel, request models.NotificationRequest) error {
	cert, err := certificate.FromP12File(s.certificate, "")
	if err != nil {
		s.logger.Errorf("failed to read certificate: %v", err)
		return fmt.Errorf("failed to read certificate: %w", err)
	}

	notification := &apns2.Notification{}
	notification.DeviceToken = device.Token
	notification.Topic = appID
	notification.Payload = []byte(`{"aps":{"alert":"Hello!"}}`) // See Payload section below

	client := apns2.NewClient(cert).Production()
	res, err := client.PushWithContext(ctx, notification)
	if err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}
	if res.Sent() {
		return nil
	}
	s.logger.Errorf("failed to send notification: %v %v", res.StatusCode, res.Reason)
	if res.StatusCode == 410 || res.StatusCode == 400 {
		// Unregister the device
		if err := s.db.UnregisterDevice(ctx, device.VaultId, device.Token); err != nil {
			s.logger.Errorf("failed to unregister device: %v", err)
		}
	}
	return nil
}

func (s *NotificationService) processAndroidNotification(ctx context.Context, device models.DeviceDBModel, request models.NotificationRequest) error {
	return nil
}
