package g09fixture

// This file is an offline capacity-fixture protocol. It records no business
// state and never opens a database, starts a process, or calls an adapter.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"
)

type CapacityFixtureState string

const (
	CapacityCollecting CapacityFixtureState = "collecting"
	CapacitySealed     CapacityFixtureState = "sealed"
	CapacityDraining   CapacityFixtureState = "draining"
	CapacityVerified   CapacityFixtureState = "verified"
	CapacityFailed     CapacityFixtureState = "failed"
	CapacityCleaned    CapacityFixtureState = "cleaned"
)

// CapacityTaskProjection is a read-only, detached view of one queue entity.
// A provision task uses Kind=provision and an email/SMS task uses its channel.
type CapacityTaskProjection struct {
	FlowID      string     `json:"flowId"`
	Kind        string     `json:"kind"`
	ID          int64      `json:"id"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	MaxAttempts *int       `json:"maxAttempts,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	AvailableAt time.Time  `json:"availableAt"`
	LockedAt    *time.Time `json:"lockedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

type CapacityDrainVerification struct {
	Status     string                   `json:"status"` // PENDING, PASS, or FAIL.
	Complete   bool                     `json:"complete"`
	Sequence   int                      `json:"sequence"`
	ObservedAt time.Time                `json:"observedAt"`
	Tasks      []CapacityTaskProjection `json:"tasks,omitempty"`
	Failure    string                   `json:"failure,omitempty"`
}

type CapacityRequest struct {
	RunID      string                   `json:"runId"`
	Sequence   uint64                   `json:"sequence"`
	Action     string                   `json:"action"`
	Sources    []CohortSourceRecord     `json:"sources,omitempty"`
	Tasks      []CapacityTaskProjection `json:"tasks,omitempty"`
	ObservedAt time.Time                `json:"observedAt,omitempty"`
}

type CapacityResponse struct {
	RunID        string                     `json:"runId"`
	Sequence     uint64                     `json:"sequence"`
	Action       string                     `json:"action"`
	OK           bool                       `json:"ok"`
	State        CapacityFixtureState       `json:"state"`
	Registered   int                        `json:"registered"`
	Verification *CapacityDrainVerification `json:"verification,omitempty"`
	Error        string                     `json:"error,omitempty"`
}

type CapacityFixture struct {
	mu           sync.Mutex
	applyMu      sync.Mutex
	runID        string
	state        CapacityFixtureState
	sequence     uint64
	sources      []CohortSourceRecord
	cohort       Cohort
	verification *CapacityDrainVerification
}

var capacityRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// NewCapacityFixture only validates and freezes the run identity. It performs
// no I/O, including no connection attempt to PostgreSQL.
func NewCapacityFixture(runID string) (*CapacityFixture, error) {
	if !capacityRunID.MatchString(runID) {
		return nil, errors.New("capacity_run_id_invalid")
	}
	return &CapacityFixture{runID: runID, state: CapacityCollecting}, nil
}

func (f *CapacityFixture) RunID() string { f.mu.Lock(); defer f.mu.Unlock(); return f.runID }

func (f *CapacityFixture) State() CapacityFixtureState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *CapacityFixture) RegisteredSources() []CohortSourceRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CohortSourceRecord(nil), f.sources...)
}

// RegisterSources accepts source rows in bounded chunks. It never infers a
// task as accepted; SealSources is the only operation that establishes the
// fixed 300 purchase + 150 ticket source denominator.
func (f *CapacityFixture) RegisterSources(records []CohortSourceRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != CapacityCollecting {
		return errors.New("capacity_registration_closed")
	}
	if len(records) == 0 || len(records) > 450-len(f.sources) {
		return errors.New("capacity_registration_count_invalid")
	}
	seen := make(map[string]bool, len(f.sources)+len(records))
	for _, r := range f.sources {
		for _, key := range capacitySourceIdentityKeys(r) {
			seen[key] = true
		}
	}
	for _, r := range records {
		if err := validateCapacitySource(r); err != nil {
			return err
		}
		for _, key := range capacitySourceIdentityKeys(r) {
			if seen[key] {
				return errors.New("capacity_registration_duplicate_entity")
			}
			seen[key] = true
		}
	}
	f.sources = append(f.sources, records...)
	return nil
}

// SealSources delegates denominator and cross-entity validation to the
// existing Cohort contract. A failed seal latches the fixture and cannot be
// repaired by later registration.
func (f *CapacityFixture) SealSources() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != CapacityCollecting {
		return errors.New("capacity_seal_invalid_state")
	}
	c, err := SealCohortSources(f.sources)
	if err != nil {
		f.state = CapacityFailed
		return errors.New("capacity_seal_invalid")
	}
	f.cohort = c
	f.state = CapacitySealed
	return nil
}

func (f *CapacityFixture) BeginDrain() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != CapacitySealed {
		return errors.New("capacity_drain_invalid_state")
	}
	f.state = CapacityDraining
	return nil
}

// VerifyDrain checks a complete read-only projection. Every expected entity
// must be present exactly once. PENDING is an honest incomplete result; only a
// projection with all terminal tasks transitions the fixture to VERIFIED.
func (f *CapacityFixture) VerifyDrain(observedAt time.Time, tasks []CapacityTaskProjection) (CapacityDrainVerification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != CapacityDraining {
		return CapacityDrainVerification{}, errors.New("capacity_verify_invalid_state")
	}
	if !cohortUTC(observedAt) {
		f.state = CapacityFailed
		return CapacityDrainVerification{}, errors.New("capacity_observed_at_invalid")
	}
	expected := expectedCapacityTasks(f.cohort)
	if len(expected) != 600 || len(tasks) != len(expected) {
		f.state = CapacityFailed
		return CapacityDrainVerification{}, errors.New("capacity_task_denominator_invalid")
	}
	seen := make(map[string]bool, len(tasks))
	complete := true
	for _, task := range tasks {
		key := task.Kind + ":" + fmt.Sprint(task.ID)
		want, ok := expected[key]
		if !ok || seen[key] || task.FlowID != want.FlowID || task.ID <= 0 {
			f.state = CapacityFailed
			return CapacityDrainVerification{}, errors.New("capacity_task_identity_invalid")
		}
		seen[key] = true
		if task.CreatedAt.Before(want.AcceptedAt) || task.CreatedAt.After(observedAt) {
			f.state = CapacityFailed
			return CapacityDrainVerification{}, errors.New("capacity_task_time_invalid")
		}
		outbox := task.Kind == "email" || task.Kind == "sms"
		if !outbox && (task.MaxAttempts != nil || task.ExpiresAt != nil) {
			f.state = CapacityFailed
			return CapacityDrainVerification{}, errors.New("capacity_provision_fields_invalid")
		}
		observation := CohortTaskObservation{FlowID: task.FlowID, Kind: task.Kind, Status: task.Status, ID: task.ID, Attempts: task.Attempts, MaxAttempts: task.MaxAttempts, CreatedAt: task.CreatedAt, AvailableAt: task.AvailableAt, LockedAt: task.LockedAt, FinishedAt: task.FinishedAt, ExpiresAt: task.ExpiresAt}
		if err := checkCohortTask(observation, observedAt, outbox); err != nil {
			f.state = CapacityFailed
			return CapacityDrainVerification{}, errors.New("capacity_task_state_invalid")
		}
		if task.Status != "succeeded" && task.Status != "sent" {
			complete = false
		}
	}
	if len(seen) != len(expected) {
		f.state = CapacityFailed
		return CapacityDrainVerification{}, errors.New("capacity_task_missing")
	}
	verification := CapacityDrainVerification{Status: "PENDING", Complete: false, Sequence: int(f.sequence), ObservedAt: observedAt, Tasks: cloneCapacityTasks(tasks)}
	if complete {
		verification.Status = "PASS"
		verification.Complete = true
		f.state = CapacityVerified
	}
	f.verification = cloneCapacityVerification(verification)
	return verification, nil
}

// Abort records an explicit failed fixture outcome. It does not terminate a
// worker, remove a schema, or alter any product state.
func (f *CapacityFixture) Abort() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == CapacityCleaned {
		return errors.New("capacity_abort_after_cleanup")
	}
	f.state = CapacityFailed
	return nil
}

// Cleanup is an ownership/state guard only. The caller remains responsible
// for deleting its own temporary files or schema through a separate verified
// mechanism; this method deliberately performs no cleanup side effect.
func (f *CapacityFixture) Cleanup() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != CapacityVerified && f.state != CapacityFailed {
		return errors.New("capacity_cleanup_incomplete")
	}
	f.state = CapacityCleaned
	return nil
}

// Apply executes one already decoded protocol request. A well-shaped request
// consumes its sequence even when the requested state transition fails; the
// caller must not replay it after an execution error.
func (f *CapacityFixture) Apply(req CapacityRequest) (CapacityResponse, error) {
	f.applyMu.Lock()
	defer f.applyMu.Unlock()
	f.mu.Lock()
	if req.RunID != f.runID || f.sequence == ^uint64(0) || req.Sequence != f.sequence+1 {
		f.mu.Unlock()
		return CapacityResponse{}, errors.New("capacity_request_sequence_invalid")
	}
	if err := validateCapacityRequestValue(req); err != nil {
		f.mu.Unlock()
		return CapacityResponse{}, err
	}
	f.sequence = req.Sequence
	f.mu.Unlock()
	response := CapacityResponse{RunID: req.RunID, Sequence: req.Sequence, Action: req.Action, OK: true}
	var err error
	switch req.Action {
	case "register-sources":
		err = f.RegisterSources(req.Sources)
	case "seal-sources":
		err = f.SealSources()
	case "begin-drain":
		err = f.BeginDrain()
	case "verify-drain":
		var verification CapacityDrainVerification
		verification, err = f.VerifyDrain(req.ObservedAt, req.Tasks)
		if err == nil {
			response.Verification = &verification
		}
	case "abort":
		err = f.Abort()
	case "cleanup":
		err = f.Cleanup()
	}
	response.State = f.State()
	response.Registered = len(f.RegisteredSources())
	if err != nil {
		response.OK = false
		response.Error = err.Error()
	}
	return response, err
}

type expectedCapacityTask struct {
	FlowID     string
	AcceptedAt time.Time
}

func expectedCapacityTasks(c Cohort) map[string]expectedCapacityTask {
	out := make(map[string]expectedCapacityTask, 600)
	for _, source := range c.sources {
		if source.Kind == "ticket" {
			out["email:"+fmt.Sprint(source.EmailOutboxID)] = expectedCapacityTask{FlowID: source.FlowID, AcceptedAt: source.AcceptedAtUTC}
			out["sms:"+fmt.Sprint(source.SMSOutboxID)] = expectedCapacityTask{FlowID: source.FlowID, AcceptedAt: source.AcceptedAtUTC}
		} else {
			out["provision:"+fmt.Sprint(source.ProvisionJobID)] = expectedCapacityTask{FlowID: source.FlowID, AcceptedAt: source.AcceptedAtUTC}
		}
	}
	return out
}

func validateCapacitySource(r CohortSourceRecord) error {
	if !cohortFlow.MatchString(r.FlowID) || r.CustomerID <= 0 || !cohortUTC(r.AcceptedAtUTC) {
		return errors.New("capacity_source_identity_invalid")
	}
	switch r.Kind {
	case "simulation", "supplier":
		if r.ProductID <= 0 || r.OrderID <= 0 || r.InvoiceID <= 0 || r.OrderItemID <= 0 || r.ServiceID <= 0 || r.ProvisionJobID <= 0 {
			return errors.New("capacity_purchase_identity_invalid")
		}
	case "ticket":
		if r.TicketID <= 0 || r.EventID <= 0 || r.StaffID <= 0 || r.DepartmentID <= 0 || r.EmailDeliveryID <= 0 || r.EmailOutboxID <= 0 || r.SMSDeliveryID <= 0 || r.SMSOutboxID <= 0 || r.StaffID == r.CustomerID {
			return errors.New("capacity_ticket_identity_invalid")
		}
	default:
		return errors.New("capacity_source_kind_invalid")
	}
	return nil
}

func capacitySourceIdentityKeys(r CohortSourceRecord) []string {
	keys := []string{"flow:" + r.FlowID}
	switch r.Kind {
	case "simulation", "supplier":
		for _, item := range []struct {
			table string
			id    int64
		}{
			{"order", r.OrderID}, {"invoice", r.InvoiceID}, {"item", r.OrderItemID}, {"service", r.ServiceID}, {"job", r.ProvisionJobID},
		} {
			keys = append(keys, item.table+":"+fmt.Sprint(item.id))
		}
	case "ticket":
		for _, item := range []struct {
			table string
			id    int64
		}{
			{"ticket", r.TicketID}, {"ticket_customer", r.CustomerID}, {"event", r.EventID}, {"staff", r.StaffID}, {"department", r.DepartmentID},
			{"delivery", r.EmailDeliveryID}, {"delivery", r.SMSDeliveryID}, {"email", r.EmailOutboxID}, {"sms", r.SMSOutboxID},
		} {
			keys = append(keys, item.table+":"+fmt.Sprint(item.id))
		}
	}
	return keys
}

func cloneCapacityTasks(in []CapacityTaskProjection) []CapacityTaskProjection {
	out := make([]CapacityTaskProjection, len(in))
	copy(out, in)
	for i := range out {
		if in[i].MaxAttempts != nil {
			n := *in[i].MaxAttempts
			out[i].MaxAttempts = &n
		}
		if in[i].LockedAt != nil {
			v := *in[i].LockedAt
			out[i].LockedAt = &v
		}
		if in[i].FinishedAt != nil {
			v := *in[i].FinishedAt
			out[i].FinishedAt = &v
		}
		if in[i].ExpiresAt != nil {
			v := *in[i].ExpiresAt
			out[i].ExpiresAt = &v
		}
	}
	return out
}

func cloneCapacityVerification(in CapacityDrainVerification) *CapacityDrainVerification {
	out := in
	out.Tasks = cloneCapacityTasks(in.Tasks)
	return &out
}

// ValidateCapacityRequest provides a strict, independent request contract.
// It does not advance a fixture sequence; Apply does that after validation.
func ValidateCapacityRequest(raw []byte, runID string, last uint64) (CapacityRequest, error) {
	var req CapacityRequest
	if len(raw) > 512*1024 || !capacityRunID.MatchString(runID) {
		return req, errors.New("capacity_request_invalid")
	}
	if rejectCapacityDuplicateKeys(raw) != nil {
		return req, errors.New("capacity_request_invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
		return req, errors.New("capacity_request_invalid")
	}
	if req.RunID != runID || last == ^uint64(0) || req.Sequence != last+1 {
		return req, errors.New("capacity_request_sequence_invalid")
	}
	if err := validateCapacityRequestValue(req); err != nil {
		return req, err
	}
	return req, nil
}

func validateCapacityRequestValue(req CapacityRequest) error {
	switch req.Action {
	case "register-sources":
		if len(req.Sources) == 0 || len(req.Tasks) != 0 || !req.ObservedAt.IsZero() {
			return errors.New("capacity_request_shape_invalid")
		}
	case "seal-sources", "begin-drain", "abort", "cleanup":
		if len(req.Sources) != 0 || len(req.Tasks) != 0 || !req.ObservedAt.IsZero() {
			return errors.New("capacity_request_shape_invalid")
		}
	case "verify-drain":
		if len(req.Sources) != 0 || len(req.Tasks) == 0 || !cohortUTC(req.ObservedAt) {
			return errors.New("capacity_request_shape_invalid")
		}
	default:
		return errors.New("capacity_request_action_invalid")
	}
	return nil
}

func rejectCapacityDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate_capacity_key")
				}
				seen[name] = true
			}
			if err := walk(); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("capacity_json_trailing")
	}
	return nil
}
