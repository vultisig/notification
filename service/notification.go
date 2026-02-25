package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/DataDog/datadog-go/statsd"
	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/hibiken/asynq"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/payload"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/option"

	"github.com/vultisig/notification/contexthelper"
	"github.com/vultisig/notification/models"
	"github.com/vultisig/notification/storage"
)

const appID = "com.vultisig.wallet"

type NotificationService struct {
	logger            *logrus.Logger
	sdClient          *statsd.Client
	db                *storage.Database
	imageServer       string
	certificate       string
	password          string
	isProd            bool
	firebaseMessaging *messaging.Client
	vapidPublicKey    string
	vapidPrivateKey   string
	vapidSubscriber   string
}

func NewNotificationService(sdClient *statsd.Client,
	db *storage.Database,
	imageServer, certificate, password string, isProd bool,
	firebaseCredentials string,
	vapidPublicKey, vapidPrivateKey, vapidSubscriber string) (*NotificationService, error) {
	if sdClient == nil {
		return nil, fmt.Errorf("sdClient is nil")
	}
	if db == nil {
		return nil, fmt.Errorf("db is nil")
	}
	svc := &NotificationService{
		logger:          logrus.WithField("service", "notification").Logger,
		sdClient:        sdClient,
		db:              db,
		imageServer:     imageServer,
		certificate:     certificate,
		password:        password,
		isProd:          isProd,
		vapidPublicKey:  vapidPublicKey,
		vapidPrivateKey: vapidPrivateKey,
		vapidSubscriber: vapidSubscriber,
	}
	if firebaseCredentials != "" {
		app, err := firebase.NewApp(context.Background(), nil, option.WithCredentialsFile(firebaseCredentials))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize firebase app: %w", err)
		}
		msgClient, err := app.Messaging(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to get firebase messaging client: %w", err)
		}
		svc.firebaseMessaging = msgClient
	}
	return svc, nil
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
	start := time.Now()
	defer s.measureTime("notification.handle.duration", start, []string{})

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

	s.logger.Infof("Processing notification for user: %s, message: %s", request.VaultId, request.VaultName)
	if err := s.processNotificationRequest(ctx, request); err != nil {
		s.logger.Errorf("failed to process notification: %v", err)
	}
	return nil
}

func (s *NotificationService) processNotificationRequest(ctx context.Context, request models.NotificationRequest) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}

	deviceRegistration, err := s.db.GetRegisteredDevices(ctx, request.VaultId, request.LocalPartyId)
	if err != nil {
		s.logger.Errorf("failed to get registered devices: %v", err)
		return fmt.Errorf("failed to get registered devices: %w", err)
	}
	if len(deviceRegistration) == 0 {
		s.logger.Infof("No registered devices found for vaultId: %s", request.VaultId)
		return nil
	}

	for _, device := range deviceRegistration {
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
		if strings.EqualFold(device.DeviceType, "web") {
			if err := s.processWebPushNotification(ctx, device, request); err != nil {
				s.logger.Errorf("failed to process web push notification: %v", err)
			}
		}
	}
	return nil
}

func (s *NotificationService) processAppleNotification(ctx context.Context, device models.DeviceDBModel, request models.NotificationRequest) error {
	defer s.measureTime("notification.apple.duration", time.Now(), []string{})
	cert, err := certificate.FromP12File(s.certificate, s.password)
	if err != nil {
		s.logger.Errorf("failed to read certificate: %v", err)
		return fmt.Errorf("failed to read certificate: %w", err)
	}

	notification := &apns2.Notification{}
	notification.DeviceToken = device.Token
	notification.Topic = appID
	p := payload.NewPayload().Alert(nil).
		AlertTitle("Vultisig Keysign request").
		AlertSubtitle("Vault: "+request.VaultName).
		Custom("deeplink", request.QRCodeData).
		Sound("default")
	notification.Payload = p // See Payload section below

	var client *apns2.Client
	if s.isProd {
		client = apns2.NewClient(cert).Production()
	} else {
		client = apns2.NewClient(cert).Development()
	}
	res, err := client.PushWithContext(ctx, notification)
	if err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}
	if res.Sent() {
		return nil
	}
	s.logger.Errorf("failed to send notification: %d %s, vault: %s", res.StatusCode, res.Reason, request.VaultName)
	if res.StatusCode == 410 || res.StatusCode == 400 {
		// Unregister the device
		if err := s.db.UnregisterDevice(ctx, device.VaultId, device.Token); err != nil {
			s.logger.Errorf("failed to unregister device: %v", err)
		}
	}
	return nil
}

func (s *NotificationService) processAndroidNotification(ctx context.Context, device models.DeviceDBModel, request models.NotificationRequest) error {
	defer s.measureTime("notification.android.duration", time.Now(), []string{})
	if s.firebaseMessaging == nil {
		s.logger.Warn("firebase messaging client not configured, skipping android notification")
		return nil
	}
	msg := &messaging.Message{
		Notification: &messaging.Notification{
			Title: "Vultisig Keysign request",
			Body:  "Vault: " + request.VaultName,
		},
		Data: map[string]string{
			"message": request.QRCodeData,
		},
		Token: device.Token,
	}
	_, err := s.firebaseMessaging.Send(ctx, msg)
	if err != nil {
		if messaging.IsUnregistered(err) {
			if unregErr := s.db.UnregisterDevice(ctx, device.VaultId, device.Token); unregErr != nil {
				s.logger.Errorf("failed to unregister device: %v", unregErr)
			}
			return nil
		}
		return fmt.Errorf("failed to send android notification: %w", err)
	}
	return nil
}

func (s *NotificationService) processWebPushNotification(ctx context.Context, device models.DeviceDBModel, request models.NotificationRequest) error {
	defer s.measureTime("notification.web.duration", time.Now(), []string{})

	var subscription webpush.Subscription
	if err := json.Unmarshal([]byte(device.Token), &subscription); err != nil {
		s.logger.Errorf("failed to unmarshal web push subscription for vault %s: %v", device.VaultId, err)
		if unregErr := s.db.UnregisterDevice(ctx, device.VaultId, device.Token); unregErr != nil {
			s.logger.Errorf("failed to unregister device with invalid subscription: %v", unregErr)
		}
		return fmt.Errorf("failed to unmarshal web push subscription: %w", err)
	}

	payloadData := map[string]string{
		"title":    "Vultisig Keysign request",
		"subtitle": "Vault: " + request.VaultName,
		"body":     request.QRCodeData,
	}
	payloadBytes, err := json.Marshal(payloadData)
	if err != nil {
		return fmt.Errorf("failed to marshal web push payload: %w", err)
	}

	resp, err := webpush.SendNotification(payloadBytes, &subscription, &webpush.Options{
		Subscriber:      s.vapidSubscriber,
		VAPIDPublicKey:  s.vapidPublicKey,
		VAPIDPrivateKey: s.vapidPrivateKey,
		TTL:             60,
		Urgency:         webpush.UrgencyHigh,
	})
	if err != nil {
		return fmt.Errorf("failed to send web push notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		s.logger.Infof("web push subscription expired (HTTP %d) for vault %s, unregistering", resp.StatusCode, device.VaultId)
		if err := s.db.UnregisterDevice(ctx, device.VaultId, device.Token); err != nil {
			s.logger.Errorf("failed to unregister expired web push device: %v", err)
		}
		return nil
	}

	if resp.StatusCode >= 400 {
		s.logger.Errorf("web push notification failed with HTTP %d for vault %s", resp.StatusCode, device.VaultId)
	}

	return nil
}
