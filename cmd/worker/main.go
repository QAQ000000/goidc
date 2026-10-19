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
	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/modules/manifest"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/scheduler"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if handled, err := manifest.WriteCLI(os.Args[1:], os.Stdout, "worker"); handled {
		if err != nil {
			log.Error("module manifest unavailable", "error", err)
			os.Exit(1)
		}
		return
	}
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
	db.FileStorage = cfg.FileStorage
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
	paymentFallback := store.PaymentConfiguration{PaymentSettings: store.PaymentSettings{Enabled: true, MockEnabled: cfg.PaymentMockEnabled, APIURL: cfg.EpayAPIURL, PID: cfg.EpayPID, Types: cfg.EpayTypes}, Key: cfg.EpayKey, PublicOrigin: cfg.PublicOrigin, MockSecret: cfg.PaymentGatewaySecret}
	scheduled := store.NewScheduledTaskRunner(db, log, paymentFallback)
	runtimeCtx, cancelRuntime := context.WithTimeout(ctx, 2*time.Second)
	runtime, runtimeErr := db.StartWorkerRuntime(runtimeCtx, log)
	cancelRuntime()
	if runtimeErr != nil {
		log.Error("worker runtime registration failed")
	}
	// Registered before Wait defers: stop is persisted only after every lane exits.
	defer runtime.Stop()
	paymentSchedule, _ := scheduler.Lookup(string(scheduler.AutomaticPaymentReconciliation))
	notificationSchedule, _ := scheduler.Lookup(string(scheduler.AffiliateSettlement))
	renewalSchedule, _ := scheduler.Lookup(string(scheduler.AutomaticRenewals))
	var paymentWorker sync.WaitGroup
	{
		paymentWorker.Add(1)
		go func() {
			defer paymentWorker.Done()
			runObservedScheduledLane(ctx, paymentSchedule, runtime.Lane("payment"), func(ctx context.Context) {
				log := correlation.Logger(ctx, log)
				if _, checkErr := scheduled.Run(ctx, scheduler.AutomaticPaymentReconciliation); checkErr != nil && ctx.Err() == nil {
					log.Error("automatic payment reconciliation failed", "error", checkErr)
				}
			})
		}()
	}
	defer paymentWorker.Wait()
	var fileWorker sync.WaitGroup
	fileWorker.Add(1)
	go func() {
		defer fileWorker.Done()
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				runWorkerPass(ctx, runtime.Lane("file_cleanup"), func(ctx context.Context) {
					if _, cleanupErr := db.CollectFileObjects(ctx, 20); cleanupErr != nil && ctx.Err() == nil {
						correlation.Logger(ctx, log).Error("file object cleanup failed", "error", cleanupErr)
					}
				})
			}
		}
	}()
	defer fileWorker.Wait()
	var notificationWorker sync.WaitGroup
	notificationWorker.Add(1)
	go func() {
		defer notificationWorker.Done()
		runObservedScheduledLane(ctx, notificationSchedule, runtime.Lane("notification_schedule"), func(ctx context.Context) { runNotificationSchedules(ctx, scheduled, correlation.Logger(ctx, log)) })
	}()
	defer notificationWorker.Wait()
	var centerWorker sync.WaitGroup
	centerWorker.Add(1)
	go func() {
		defer centerWorker.Done()
		lane := runtime.Lane("notification_center")
		notification.RunNotificationCenter(lane.Context(ctx), db, log, lane.Observe)
	}()
	defer centerWorker.Wait()
	var certificationWorker sync.WaitGroup
	certificationWorker.Add(1)
	go func() {
		defer certificationWorker.Done()
		lane := runtime.Lane("certification")
		runCertifications(lane.Context(ctx), db, log, lane.Observe)
	}()
	defer certificationWorker.Wait()
	// A full renewal pass can outlast the provisioning poll interval. Run it
	// independently so one day's renewal work does not delay delivery or mail.
	var renewalWorker sync.WaitGroup
	renewalWorker.Add(1)
	go func() {
		defer renewalWorker.Done()
		runObservedScheduledLane(ctx, renewalSchedule, runtime.Lane("auto_renew"), func(ctx context.Context) {
			log := correlation.Logger(ctx, log)
			if _, renewErr := scheduled.Run(ctx, scheduler.AutomaticRenewals); renewErr != nil && ctx.Err() == nil {
				log.Error("auto renewal processing failed", "error", renewErr)
			}
		})
	}()
	defer renewalWorker.Wait()
	log.Info("provision worker started")
	provisionSchedule, _ := scheduler.Lookup(string(scheduler.SupplierSyncSchedule))
	ticker := time.NewTicker(provisionSchedule.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runWorkerPass(ctx, runtime.Lane("provision"), func(ctx context.Context) {
				baseLog := log
				log := correlation.Logger(ctx, baseLog)
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
					jobCtx := workerJobContext(ctx, job.CorrelationID)
					log := correlation.Logger(jobCtx, baseLog).With("job_id", job.ID, "attempts", job.Attempts, "attempt_id", correlation.NewID(), "queue", "sms")
					store.WorkerRuntimeProgress(ctx)
					log.Info("job attempt started")
					active, checkErr := db.SMSJobActive(jobCtx, *job)
					if checkErr != nil || !active {
						_ = db.RecoverStaleSMSJobs(ctx)
						continue
					}
					smsRuntime, resolveErr := db.ResolveSMSConfigForInstance(jobCtx, job.ChannelInstanceID)
					var sendErr error
					evidence := store.SMSDeliveryEvidence{Provider: smsRuntime.Provider, Outcome: "configuration_rejected"}
					switch {
					case resolveErr != nil:
						sendErr = resolveErr
					case !smsRuntime.Enabled:
						sendErr = errors.New("SMS channel disabled")
					default:
						evidence, sendErr = notification.SendSMSWithEvidence(jobCtx, smsRuntime, *job)
					}
					// Keep the response durable before marking this attempt complete.
					if evidenceErr := db.SaveSMSDeliveryEvidence(jobCtx, *job, evidence); evidenceErr != nil {
						log.Error("SMS evidence persistence failed")
						continue
					}
					if completeErr := db.CompleteSMSJob(jobCtx, *job, sendErr); completeErr != nil {
						log.Error("SMS completion failed")
					}
					if sendErr != nil {
						log.Error("SMS delivery failed")
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
					jobCtx := workerJobContext(ctx, job.CorrelationID)
					log := correlation.Logger(jobCtx, baseLog).With("job_id", job.ID, "attempts", job.Attempts, "attempt_id", correlation.NewID(), "queue", "email")
					store.WorkerRuntimeProgress(ctx)
					log.Info("job attempt started")
					active, checkErr := db.EmailJobActive(jobCtx, *job)
					if checkErr != nil || !active {
						_ = db.RecoverStaleEmailJobs(ctx)
						continue
					}
					implementation, mailConfig, resolveErr := db.ResolveMailConfigForInstance(jobCtx, job.ChannelInstanceID, smtpFallback)
					var sendErr error
					if resolveErr != nil {
						sendErr = resolveErr
					} else {
						sendErr = notification.SendMail(implementation, mailConfig, *job)
					}
					if completeErr := db.CompleteEmailJob(jobCtx, *job, sendErr); completeErr != nil {
						log.Error("email completion failed", "error", completeErr)
					}
					if sendErr != nil {
						log.Error("email delivery failed", "error", sendErr)
					}
				}
				if err = db.RecoverStaleProvisionJobs(ctx); err != nil {
					log.Error("stale job recovery failed", "error", err)
					return
				}
				if _, err = scheduled.Run(ctx, scheduler.SupplierSyncSchedule); err != nil {
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
					jobCtx := workerJobContext(ctx, job.CorrelationID)
					log := correlation.Logger(jobCtx, baseLog).With("job_id", job.ID, "attempts", job.Attempts, "attempt_id", correlation.NewID(), "queue", "provision")
					store.WorkerRuntimeProgress(ctx)
					log.Info("job attempt started", "action", job.Action)
					if processErr := db.ProcessProvisionJob(jobCtx, *job); processErr != nil {
						log.Error("provision job failed", "service_id", job.ServiceID, "action", job.Action, "error", processErr)
					}
				}
			})
		}
	}
}

func runScheduledLane(ctx context.Context, task scheduler.Descriptor, run func()) {
	timer := time.NewTicker(task.Interval)
	defer timer.Stop()
	runScheduledTicks(ctx, task.Startup, timer.C, run)
}

func runScheduledTicks(ctx context.Context, startup bool, ticks <-chan time.Time, run func()) {
	if startup && ctx.Err() == nil {
		run()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-ticks:
			if !open || ctx.Err() != nil {
				return
			}
			run()
		}
	}
}

func runNotificationSchedules(ctx context.Context, scheduled *scheduler.Runner, log *slog.Logger) {
	if released, settleErr := scheduled.Run(ctx, scheduler.AffiliateSettlement); settleErr != nil && ctx.Err() == nil {
		log.Error("affiliate commission settlement failed", "error", settleErr)
	} else if released > 0 {
		log.Info("affiliate commissions released", "count", released)
	}
	if queued, scheduleErr := scheduled.Run(ctx, scheduler.ServiceExpiryNotifications); scheduleErr != nil && ctx.Err() == nil {
		log.Error("service expiry notification scheduling failed", "error", scheduleErr)
	} else if queued > 0 {
		log.Info("service expiry notifications queued", "count", queued)
	}
	if queued, scheduleErr := scheduled.Run(ctx, scheduler.BillingNotifications); scheduleErr != nil && ctx.Err() == nil {
		log.Error("billing notification scheduling failed", "error", scheduleErr)
	} else if queued > 0 {
		log.Info("billing notifications queued", "count", queued)
	}
	if queued, scheduleErr := scheduled.Run(ctx, scheduler.OverdueServiceSuspensions); scheduleErr != nil && ctx.Err() == nil {
		log.Error("overdue suspension scheduling failed", "error", scheduleErr)
	} else if queued > 0 {
		log.Info("overdue service suspensions queued", "count", queued)
	}
}
