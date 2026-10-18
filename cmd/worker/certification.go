package main

import (
	"context"
	"log/slog"
	"time"
)

type certificationProcessor interface {
	ProcessCertificationJob(context.Context) (bool, error)
}

// Keep identity-provider latency outside the provisioning, notification and
// payment loops. The store owns per-call deadlines and durable lease recovery.
func runCertifications(ctx context.Context, processor certificationProcessor, log *slog.Logger) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		for range 10 {
			if ctx.Err() != nil {
				return
			}
			processed, err := processor.ProcessCertificationJob(ctx)
			if err != nil {
				if ctx.Err() == nil {
					// Provider errors and identity data must never reach worker logs.
					log.Error("certification processing failed")
				}
				break
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
