package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type certificationProcessorFunc func(context.Context) (bool, error)

func (f certificationProcessorFunc) ProcessCertificationJob(ctx context.Context) (bool, error) {
	return f(ctx)
}

type certificationLogWriter struct {
	bytes.Buffer
	written chan struct{}
}

func (w *certificationLogWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	select {
	case w.written <- struct{}{}:
	default:
	}
	return n, err
}

func TestCertificationWorkerCancellationAndErrorPrivacy(t *testing.T) {
	for _, mode := range []string{"during_call", "after_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{})
			done := make(chan struct{})
			output := certificationLogWriter{written: make(chan struct{}, 1)}
			processor := certificationProcessorFunc(func(ctx context.Context) (bool, error) {
				close(started)
				if mode == "during_call" {
					<-ctx.Done()
				}
				return true, errors.New("private identity and provider credential")
			})
			go func() {
				defer close(done)
				runCertifications(ctx, processor, slog.New(slog.NewJSONHandler(&output, nil)))
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("certification loop did not start")
			}
			if mode == "after_error" {
				select {
				case <-output.written:
				case <-time.After(time.Second):
					t.Fatal("certification loop did not record its safe error")
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("certification loop did not stop with its parent")
			}
			if strings.Contains(output.String(), "private identity") || strings.Contains(output.String(), "provider credential") {
				t.Fatal("worker log contains private provider material")
			}
		})
	}
}
