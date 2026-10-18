package captcha

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

func TestPNGIssueAndLegacyAnswerHash(t *testing.T) {
	p := PNG()
	issued, err := p.Issue(t.Context(), Request{Action: "login"})
	if err != nil || ValidateIssued(issued) != nil || issued.Challenge.Kind != Image || issued.Challenge.Width != 190 || issued.Challenge.Height != 64 || len(issued.State) != 64 {
		t.Fatal("invalid PNG issue", err)
	}
	if _, err := hex.DecodeString(string(issued.State)); err != nil {
		t.Fatal("PNG state does not preserve the legacy hash format")
	}
	digest := sha256.Sum256([]byte("ABC234"))
	state := []byte(hex.EncodeToString(digest[:]))
	for _, answer := range []string{"ABC234", " abc234 ", "AbC234"} {
		ok, err := p.Verify(t.Context(), Request{}, state, Proof{Kind: Image, Answer: answer})
		if !ok || err != nil {
			t.Fatal("legacy answer rejected", err)
		}
	}
	for _, proof := range []Proof{
		{Kind: Image, Answer: "ABC235"}, {Kind: Image, Answer: "ABC23I"},
		{Kind: Image, Answer: strings.Repeat(" ", 65) + "ABC234"},
		{Kind: Click, Points: []Point{{.1, .2}}}, {Kind: Widget, Token: "ABC234"},
		{Kind: Image, Answer: "ABC234", Token: "unexpected"},
	} {
		ok, err := p.Verify(t.Context(), Request{}, state, proof)
		if ok || err != nil {
			t.Fatal("invalid PNG proof accepted", proof.Kind, err)
		}
	}
	if ok, err := p.Verify(t.Context(), Request{}, []byte(strings.Repeat("z", 64)), Proof{Kind: Image, Answer: "ABC234"}); ok || err != nil {
		t.Fatal("invalid stored hash accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Issue(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatal("issue ignored canceled context")
	}
	if _, err := p.Verify(ctx, Request{}, state, Proof{Kind: Image, Answer: "ABC234"}); !errors.Is(err, context.Canceled) {
		t.Fatal("verify ignored canceled context")
	}
}

func TestIssuedChallengeValidation(t *testing.T) {
	image, err := PNG().Issue(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	click := image
	click.Challenge.Kind = Click
	click.Challenge.MaxPoints = 3
	widget := Issued{Challenge: Challenge{Kind: Widget, Renderer: "test_widget", PublicData: map[string]any{"site_key": "public"}}}
	for _, good := range []Issued{image, click, widget} {
		if err := ValidateIssued(good); err != nil {
			t.Fatal("valid challenge rejected", good.Challenge.Kind, err)
		}
	}
	checks := map[string]func(*Issued){
		"unknown kind":        func(i *Issued) { i.Challenge.Kind = "script" },
		"remote image":        func(i *Issued) { i.Challenge.Image = "https://example.test/image.png" },
		"svg image":           func(i *Issued) { i.Challenge.Image = "data:image/svg+xml;base64,PHN2Zz4=" },
		"malformed image":     func(i *Issued) { i.Challenge.Image = "data:image/png;base64,AAAA" },
		"wrong image type":    func(i *Issued) { i.Challenge.Image = strings.Replace(i.Challenge.Image, "image/png", "image/jpeg", 1) },
		"wrong dimensions":    func(i *Issued) { i.Challenge.Width++ },
		"oversize dimensions": func(i *Issued) { i.Challenge.Width = 4097 },
		"unknown renderer":    func(i *Issued) { i.Challenge.Renderer = "https://example.test/script.js" },
		"excessive state":     func(i *Issued) { i.State = make([]byte, MaxStateBytes+1) },
		"excessive prompt":    func(i *Issued) { i.Challenge.Prompt = strings.Repeat("a", 1025) },
		"excessive public": func(i *Issued) {
			i.Challenge.PublicData = map[string]any{"blob": strings.Repeat("a", MaxChallengeBytes)}
		},
		"non JSON public": func(i *Issued) { i.Challenge.PublicData = map[string]any{"value": math.Inf(1)} },
		"image points":    func(i *Issued) { i.Challenge.MaxPoints = 1 },
	}
	for name, change := range checks {
		t.Run(name, func(t *testing.T) {
			bad := image
			change(&bad)
			if !errors.Is(ValidateIssued(bad), ErrChallenge) {
				t.Fatal("invalid public/state data accepted")
			}
		})
	}
	for _, max := range []int{0, 33} {
		bad := click
		bad.Challenge.MaxPoints = max
		if ValidateIssued(bad) == nil {
			t.Fatal("unbounded click point count accepted")
		}
	}
	for _, change := range []func(*Issued){
		func(i *Issued) { i.Challenge.Renderer = "" },
		func(i *Issued) { i.Challenge.Image = image.Challenge.Image },
		func(i *Issued) { i.Challenge.Width = 190 },
		func(i *Issued) { i.Challenge.MaxPoints = 1 },
	} {
		bad := widget
		change(&bad)
		if ValidateIssued(bad) == nil {
			t.Fatal("mixed widget challenge accepted")
		}
	}
}

func TestProofDiscriminatorsAndBounds(t *testing.T) {
	for _, good := range []Proof{
		{Kind: Image, Answer: "Case sensitive vendor answer"},
		{Kind: Click, Points: []Point{{0, 0}, {1, 1}}},
		{Kind: Widget, Token: "Opaque.Token+-_="},
	} {
		if !ValidateProof(good) {
			t.Fatal("valid proof rejected", good.Kind)
		}
	}
	for _, bad := range []Proof{
		{}, {Kind: "unknown", Answer: "a"}, {Kind: Image}, {Kind: Image, Answer: " "},
		{Kind: Image, Answer: strings.Repeat("x", 1025)}, {Kind: Image, Answer: string([]byte{0xff})},
		{Kind: Image, Answer: "answer", Token: "token"}, {Kind: Image, Answer: "answer", Points: []Point{{0, 0}}},
		{Kind: Click}, {Kind: Click, Points: make([]Point, 33)},
		{Kind: Click, Points: []Point{{-.1, 0}}}, {Kind: Click, Points: []Point{{0, 1.1}}},
		{Kind: Click, Points: []Point{{math.NaN(), 0}}}, {Kind: Click, Points: []Point{{0, math.Inf(1)}}},
		{Kind: Click, Points: []Point{{0, 0}}, Answer: "a"},
		{Kind: Widget}, {Kind: Widget, Token: " "}, {Kind: Widget, Token: strings.Repeat("x", 12*1024+1)},
		{Kind: Widget, Token: "token", Answer: "answer"}, {Kind: Widget, Token: "token", Points: []Point{{0, 0}}},
	} {
		if ValidateProof(bad) {
			t.Fatal("invalid proof accepted", bad.Kind)
		}
	}
	// JSON escaping counts toward the transport bound as well as the raw field
	// length, preventing control characters from multiplying the output size.
	if ValidateProof(Proof{Kind: Widget, Token: strings.Repeat("\x00", 3000)}) {
		t.Fatal("escaped proof exceeded the serialized limit")
	}
}

type testInteractiveProvider struct{ kind Kind }

func (p testInteractiveProvider) Issue(ctx context.Context, req Request) (Issued, error) {
	if p.kind == Widget {
		return Issued{Challenge: Challenge{Kind: Widget, Renderer: "test_widget", PublicData: map[string]any{"site_key": "public-key"}}, State: []byte(req.ChallengeID)}, nil
	}
	issued, err := PNG().Issue(ctx, req)
	issued.Challenge.Kind = Click
	issued.Challenge.MaxPoints = 2
	issued.State = []byte(req.ChallengeID)
	return issued, err
}

func (p testInteractiveProvider) Verify(_ context.Context, req Request, state []byte, proof Proof) (bool, error) {
	if !ValidateProof(proof) || string(state) != req.ChallengeID || proof.Kind != p.kind {
		return false, nil
	}
	if p.kind == Widget {
		return proof.Token == "vendor-signed-token", nil
	}
	return len(proof.Points) == 2 && proof.Points[0] == (Point{.25, .5}) && proof.Points[1] == (Point{.75, .5}), nil
}

var testProviderOnce sync.Once

func registerTestProvider(t *testing.T) {
	t.Helper()
	testProviderOnce.Do(func() {
		if err := Register(channel.Descriptor{
			ID: "test_interactive", Name: "Test interactive provider", Category: channel.Captcha,
			Capabilities: []string{"click", "widget"}, SchemaVersion: 1, ContractVersion: channel.ContractVersion, InstanceMode: "profile",
			Fields: []channel.Field{
				{Key: "kind", Label: "Kind", Type: channel.Select, Required: true, Options: []string{"click", "widget"}},
				{Key: "credential", Label: "Credential", Type: channel.Text, Secret: true, Required: true},
			},
		}, func(cfg module.Config) (Provider, error) {
			if cfg.Secrets["credential"] == "invalid-sensitive-value" {
				return nil, errors.New("invalid-sensitive-value")
			}
			if cfg.Secrets["credential"] == "nil-provider" {
				return (*testInteractiveProvider)(nil), nil
			}
			return testInteractiveProvider{kind: Kind(cfg.Values["kind"].(string))}, nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCompiledClickAndWidgetProviders(t *testing.T) {
	registerTestProvider(t)
	for _, kind := range []Kind{Click, Widget} {
		t.Run(string(kind), func(t *testing.T) {
			profile := Profile{ID: "interactive", ImplementationID: "test_interactive", Config: map[string]any{"kind": string(kind)}, Secrets: map[string]string{"credential": "private-credential"}}
			m, err := NewManager([]Profile{profile}, "interactive")
			if err != nil {
				t.Fatal(err)
			}
			binding, p, err := m.Active()
			if err != nil || binding.ProfileID != "interactive" || binding.ImplementationID != "test_interactive" || len(binding.Fingerprint) != 64 {
				t.Fatal("invalid active binding", err)
			}
			req := Request{Action: "register", ChallengeID: "request-binding", ClientIP: "127.0.0.1", ExpiresAt: time.Now().Add(time.Minute)}
			issued, err := p.Issue(t.Context(), req)
			if err != nil || ValidateIssued(issued) != nil || issued.Challenge.Kind != kind {
				t.Fatal("provider issue failed", err)
			}
			proof := Proof{Kind: Click, Points: []Point{{.25, .5}, {.75, .5}}}
			if kind == Widget {
				proof = Proof{Kind: Widget, Token: "vendor-signed-token"}
			}
			resolved, err := m.Resolve(binding)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := resolved.Verify(t.Context(), req, issued.State, proof); !ok || err != nil {
				t.Fatal("valid provider proof rejected", err)
			}
			req.ChallengeID = "different-request"
			if ok, err := resolved.Verify(t.Context(), req, issued.State, proof); ok || err != nil {
				t.Fatal("proof crossed its challenge binding", err)
			}
			public, _ := json.Marshal(struct {
				Binding   Binding
				Challenge Challenge
			}{binding, issued.Challenge})
			if strings.Contains(string(public), "private-credential") {
				t.Fatal("provider credential reached public/persisted binding")
			}
		})
	}
}

func TestManagerFingerprintAndFailClosedResolution(t *testing.T) {
	registerTestProvider(t)
	profile := Profile{ID: "interactive", ImplementationID: "test_interactive", Config: map[string]any{"kind": "widget"}, Secrets: map[string]string{"credential": "original"}}
	m, err := NewManager([]Profile{profile}, "interactive")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := m.Active()
	if _, err := m.Resolve(Binding{ProfileID: BuiltinProfileID, ImplementationID: BuiltinImplementationID}); err != nil {
		t.Fatal("legacy built-in challenge rejected")
	}
	for _, bad := range []Binding{
		{}, {ProfileID: "missing", ImplementationID: "test_interactive", Fingerprint: binding.Fingerprint},
		{ProfileID: binding.ProfileID, ImplementationID: BuiltinImplementationID, Fingerprint: binding.Fingerprint},
		{ProfileID: binding.ProfileID, ImplementationID: binding.ImplementationID},
		{ProfileID: binding.ProfileID, ImplementationID: binding.ImplementationID, Fingerprint: strings.Repeat("0", 64)},
	} {
		if _, err := m.Resolve(bad); !errors.Is(err, ErrConfiguration) {
			t.Fatal("mismatched binding accepted")
		}
	}
	// A selected default can change while a retained profile keeps verifying its
	// existing challenges. Credential or config changes deliberately invalidate it.
	retained, _ := NewManager([]Profile{profile}, BuiltinProfileID)
	if _, err := retained.Resolve(binding); err != nil {
		t.Fatal("active switch invalidated retained provider")
	}
	again, _ := NewManager([]Profile{profile}, "interactive")
	againBinding, _, _ := again.Active()
	if againBinding != binding {
		t.Fatal("identical configuration has unstable fingerprint")
	}
	profile.Secrets["credential"] = "rotated"
	rotated, _ := NewManager([]Profile{profile}, "interactive")
	if _, err := rotated.Resolve(binding); !errors.Is(err, ErrConfiguration) {
		t.Fatal("credential rotation reused an old challenge")
	}
	profile.Secrets["credential"] = "original"
	profile.Config["kind"] = "click"
	changed, _ := NewManager([]Profile{profile}, "interactive")
	if _, err := changed.Resolve(binding); !errors.Is(err, ErrConfiguration) {
		t.Fatal("configuration change reused an old challenge")
	}
	removed, _ := NewManager(nil, "")
	if _, err := removed.Resolve(binding); !errors.Is(err, ErrConfiguration) {
		t.Fatal("missing provider fell back to built-in")
	}
	var nilManager *Manager
	if _, _, err := nilManager.Active(); err == nil {
		t.Fatal("nil manager supplied an active provider")
	}
	if _, err := nilManager.Resolve(binding); err == nil {
		t.Fatal("nil manager resolved a provider")
	}
}

func TestConfigurationRejectsUnknownProfilesAndMasksSecrets(t *testing.T) {
	registerTestProvider(t)
	for _, profiles := range [][]Profile{
		{{ID: BuiltinProfileID, ImplementationID: BuiltinImplementationID}},
		{{ID: "bad id", ImplementationID: BuiltinImplementationID}},
		{{ID: "custom", ImplementationID: "unknown"}},
		{{ID: "custom", ImplementationID: BuiltinImplementationID}, {ID: "custom", ImplementationID: BuiltinImplementationID}},
		{{ID: "custom", ImplementationID: BuiltinImplementationID, Config: map[string]any{"unknown": "field"}}},
		{{ID: "custom", ImplementationID: BuiltinImplementationID, Secrets: map[string]string{"unknown": "secret"}}},
		{{ID: "custom", ImplementationID: "test_interactive", Config: map[string]any{"kind": "click"}}},
	} {
		if _, err := NewManager(profiles, ""); !errors.Is(err, ErrConfiguration) {
			t.Fatal("invalid profile accepted")
		}
	}
	if _, err := NewManager(nil, "unknown"); err == nil {
		t.Fatal("unknown active profile accepted")
	}
	for _, cfg := range []module.Config{
		{Secret: "legacy-secret"}, {Payload: "untyped"}, {PublicOrigin: "https://example.test"},
		{Values: map[string]any{"unknown": true}}, {Secrets: map[string]string{"unexpected": "secret"}},
	} {
		if _, err := New(BuiltinImplementationID, cfg); !errors.Is(err, ErrConfiguration) {
			t.Fatal("invalid built-in config accepted")
		}
	}
	_, err := New("test_interactive", module.Config{Values: map[string]any{"kind": "widget"}, Secrets: map[string]string{"credential": "invalid-sensitive-value"}})
	if err != ErrConfiguration || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("factory error exposed credential")
	}
	if _, err := New(BuiltinImplementationID, module.Config{}); err != nil {
		t.Fatal("default built-in configuration rejected", err)
	}
	if _, err := channel.New(channel.Captcha, BuiltinImplementationID, module.Config{Values: map[string]any{"unexpected": "field"}}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("common registry bypassed typed factory config validation")
	}
	for _, secret := range []string{"nil-provider", strings.Repeat("x", 64*1024)} {
		if _, err := New("test_interactive", module.Config{Values: map[string]any{"kind": "widget"}, Secrets: map[string]string{"credential": secret}}); !errors.Is(err, ErrConfiguration) {
			t.Fatal("nil provider or excessive credentials accepted")
		}
	}
}
