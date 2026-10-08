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
					if _, checkErr := db.ProcessConfiguredAutomaticPaymentCheck(ctx, fallback); checkErr != nil && ctx.Err() == nil {
						log.Error("automatic payment reconciliation failed", "error", checkErr)
					}
				}
			}
		}()
	}
	defer paymentWorker.Wait()
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
				smtpConfig := notification.SMTPConfig{Host: smtpRuntime.Host, Port: smtpRuntime.Port, Username: smtpRuntime.Username, Password: smtpRuntime.Password, From: smtpRuntime.From, TLSMode: smtpRuntime.TLSMode, FromName: smtpRuntime.FromName}
				var sendErr error
				switch {
				case resolveErr != nil:
					sendErr = resolveErr
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
			if _, err = db.ProcessAutoRenewals(ctx, 20); err != nil {
				log.Error("auto renewal processing failed", "error", err)
			}
			if err = db.RecoverStaleProvisionJobs(ctx); err != nil {
				log.Error("stale job recovery failed", "error", err)
				continue
			}
			if _, err = db.EnqueuePeriodicSupplierSyncs(ctx); err != nil {
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
