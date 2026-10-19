package correlation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
)

func TestIDsAreValidatedAndContextsDoNotLeak(t *testing.T) {
	id := NewID()
	if !Valid(id) {
		t.Fatalf("invalid generated identifier %q", id)
	}
	parent := WithID(context.Background(), id)
	for _, invalid := range []string{"", "request-id", "0123456789ABCDEF0123456789ABCDEF", id + "\n", id[:31]} {
		if Valid(invalid) || ID(WithID(parent, invalid)) != "" {
			t.Fatalf("accepted invalid identifier %q", invalid)
		}
	}
	seen := sync.Map{}
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ctx := JobContext(parent, "")
			value := ID(ctx)
			if !Valid(value) || value == id {
				t.Errorf("historical attempt inherited another origin")
			}
			if _, loaded := seen.LoadOrStore(value, true); loaded {
				t.Errorf("duplicate generated identifier")
			}
		}()
	}
	workers.Wait()
	if ID(parent) != id || ID(JobContext(parent, id)) != id {
		t.Fatal("durable origin changed")
	}
}

func TestContextHandlerUsesEachCallContext(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(Handler(slog.NewJSONHandler(&output, nil)))
	first, second := NewID(), NewID()
	logger.InfoContext(WithID(context.Background(), first), "first")
	logger.With("component", "fixture").InfoContext(WithID(context.Background(), second), "second")
	logger.InfoContext(context.Background(), "plain")
	decoder := json.NewDecoder(&output)
	for i, want := range []string{first, second, ""} {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}
		got, _ := entry["correlation_id"].(string)
		if got != want {
			t.Fatalf("entry %d correlation=%q want=%q", i, got, want)
		}
	}
}

func TestErrorMetadataPreservesOriginAndErrorIdentity(t *testing.T) {
	root := errors.New("fixture")
	id := NewID()
	wrapped := WrapError(WithID(context.Background(), id), fmt.Errorf("outer: %w", root))
	wrapped = WrapError(WithID(context.Background(), NewID()), wrapped)
	if !errors.Is(wrapped, root) || wrapped.Error() != "outer: fixture" || ID(FromError(context.Background(), wrapped)) != id {
		t.Fatal("error identity, text or inner origin changed")
	}
	if WrapError(context.Background(), nil) != nil {
		t.Fatal("nil error changed")
	}
}
