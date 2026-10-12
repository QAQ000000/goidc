package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SupplierDownstreamURL = cfg.PublicOrigin
	db.Provisioners = cfg.NOKVMProfiles
	db.ProvisionEncryptionKey = cfg.ProvisionEncryptionKey
	if err = db.CheckSchema(ctx); err != nil {
		log.Error("schema invalid", "error", err)
		os.Exit(1)
	}
	if err = db.RecoverStaleProvisionJobs(ctx); err != nil {
		log.Error("stale job recovery failed", "error", err)
		os.Exit(1)
	}
	if err = db.RecoverStaleEmailJobs(ctx); err != nil {
		log.Error("stale email recovery failed", "error", err)
		os.Exit(1)
	}
	smtpFallback := store.SMTPRuntimeConfig{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, TLSMode: cfg.SMTPTLSMode, FromName: cfg.SMTPFromName}
	var paymentWorker sync.WaitGroup
	{
		paymentWorker.Add(1)
		go func() {
			defer paymentWorker.Done()
			timer := time.NewTicker(10 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					fallback := store.PaymentConfiguration{PaymentSettings: store.PaymentSettings{Enabled: true, MockEnabled: cfg.PaymentMockEnabled, APIURL: cfg.EpayAPIURL, PID: cfg.EpayPID, Types: cfg.EpayTypes}, Key: cfg.EpayKey, PublicOrigin: cfg.PublicOrigin, MockSecret: cfg.PaymentGatewaySecret}
					_, checkErr := db.RunScheduled(ctx, log, "automatic_payment_reconciliation", func(c context.Context) (int, error) {
						changed, err := db.ProcessConfiguredAutomaticPaymentCheck(c, fallback)
						if changed {
							return 1, err
						}
						return 0, err
					})
					if checkErr != nil && ctx.Err() == nil {
						log.Error("automatic payment reconciliation failed", "error", checkErr)
					}
				}
			}
		}()
	}
	defer paymentWorker.Wait()
	var notificationWorker sync.WaitGroup
	notificationWorker.Add(1)
	go func() {
		defer notificationWorker.Done()
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if released, settleErr := db.RunScheduled(ctx, log, "affiliate_settlement", func(c context.Context) (int, error) { return db.SettleAffiliateCommissions(c, 100) }); settleErr != nil && ctx.Err() == nil {
					log.Error("affiliate commission settlement failed", "error", settleErr)
				} else if released > 0 {
					log.Info("affiliate commissions released", "count", released)
				}
				if queued, scheduleErr := db.RunScheduled(ctx, log, "service_expiry_notifications", func(c context.Context) (int, error) { return db.EnqueueServiceExpiryReminders(c, 100) }); scheduleErr != nil && ctx.Err() == nil {
					log.Error("service expiry notification scheduling failed", "error", scheduleErr)
				} else if queued > 0 {
					log.Info("service expiry notifications queued", "count", queued)
				}
				if queued, scheduleErr := db.RunScheduled(ctx, log, "billing_notifications", func(c context.Context) (int, error) { return db.EnqueueBillingNotifications(c, 100) }); scheduleErr != nil && ctx.Err() == nil {
					log.Error("billing notification scheduling failed", "error", scheduleErr)
				} else if queued > 0 {
					log.Info("billing notifications queued", "count", queued)
				}
				if queued, scheduleErr := db.RunScheduled(ctx, log, "overdue_service_suspensions", func(c context.Context) (int, error) { return db.EnqueueOverdueServiceSuspensions(c, 100) }); scheduleErr != nil && ctx.Err() == nil {
					log.Error("overdue suspension scheduling failed", "error", scheduleErr)
				} else if queued > 0 {
					log.Info("overdue service suspensions queued", "count", queued)
				}
			}
		}
	}()
	defer notificationWorker.Wait()
	log.Info("provision worker started")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err = db.RecoverStaleSMSJobs(ctx); err != nil {
				log.Error("SMS stale recovery failed")
			}
			for i := 0; i < 10; i++ {
				job, claimErr := db.ClaimSMSJob(ctx)
				if errors.Is(claimErr, pgx.ErrNoRows) {
					break
				}
				if claimErr != nil {
					log.Error("SMS claim failed")
					break
				}
				active, checkErr := db.SMSJobActive(ctx, *job)
				if checkErr != nil || !active {
					_ = db.RecoverStaleSMSJobs(ctx)
					continue
				}
				smsRuntime, resolveErr := db.ResolveSMSConfigForInstance(ctx, job.ChannelInstanceID)
				var sendErr error
				switch {
				case resolveErr != nil:
					sendErr = resolveErr
				case !smsRuntime.Enabled:
					sendErr = errors.New("SMS channel disabled")
				default:
					sendErr = notification.SendSMS(ctx, smsRuntime, *job)
				}
				if completeErr := db.CompleteSMSJob(ctx, *job, sendErr); completeErr != nil {
					log.Error("SMS completion failed", "job_id", job.ID)
				}
				if sendErr != nil {
					log.Error("SMS delivery failed", "job_id", job.ID, "attempts", job.Attempts)
				}
			}
			if err = db.RecoverStaleEmailJobs(ctx); err != nil {
				log.Error("stale email recovery failed", "error", err)
			}
			for i := 0; i < 10; i++ {
				job, claimErr := db.ClaimEmailJob(ctx)
				if errors.Is(claimErr, pgx.ErrNoRows) {
					break
				}
				if claimErr != nil {
					log.Error("email claim failed", "error", claimErr)
					break
				}
				active, checkErr := db.EmailJobActive(ctx, *job)
				if checkErr != nil || !active {
					_ = db.RecoverStaleEmailJobs(ctx)
					continue
				}
				smtpRuntime, resolveErr := db.ResolveSMTPConfigForInstance(ctx, job.ChannelInstanceID, smtpFallback)
				smtpConfig, factoryErr := notification.NewMailProvider("smtp", module.Config{Values: map[string]any{"host": smtpRuntime.Host, "port": smtpRuntime.Port, "username": smtpRuntime.Username, "from": smtpRuntime.From, "tls_mode": smtpRuntime.TLSMode, "from_name": smtpRuntime.FromName}, Secret: smtpRuntime.Password})
				var sendErr error
				switch {
				case resolveErr != nil:
					sendErr = resolveErr
				case factoryErr != nil:
					sendErr = factoryErr
				case !smtpConfig.Enabled():
					sendErr = errors.New("SMTP channel disabled")
				default:
					sendErr = smtpConfig.Send(*job)
				}
				if completeErr := db.CompleteEmailJob(ctx, *job, sendErr); completeErr != nil {
					log.Error("email completion failed", "job_id", job.ID, "error", completeErr)
				}
				if sendErr != nil {
					log.Error("email delivery failed", "job_id", job.ID, "attempts", job.Attempts, "error", sendErr)
				}
			}
			if _, err = db.RunScheduled(ctx, log, "automatic_renewals", func(c context.Context) (int, error) { return db.ProcessAutoRenewals(c, 20) }); err != nil {
				log.Error("auto renewal processing failed", "error", err)
			}
			if err = db.RecoverStaleProvisionJobs(ctx); err != nil {
				log.Error("stale job recovery failed", "error", err)
				continue
			}
			if _, err = db.RunScheduled(ctx, log, "supplier_sync_schedule", func(c context.Context) (int, error) { n, e := db.EnqueuePeriodicSupplierSyncs(c); return int(n), e }); err != nil {
				log.Error("periodic supplier sync enqueue failed", "error", err)
			}
			for i := 0; i < 20; i++ {
				job, claimErr := db.ClaimProvisionJob(ctx)
				if errors.Is(claimErr, pgx.ErrNoRows) {
					break
				}
				if claimErr != nil {
					log.Error("job claim failed", "error", claimErr)
					break
				}
				if processErr := db.ProcessProvisionJob(ctx, *job); processErr != nil {
					log.Error("provision job failed", "job_id", job.ID, "service_id", job.ServiceID, "action", job.Action, "error", processErr)
				}
			}
		}
	}
}
