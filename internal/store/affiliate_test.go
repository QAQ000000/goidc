package store

import (
	"math"
	"testing"
)

func TestAffiliateIntegerCommissionAmount(t *testing.T) {
	for _, tc := range []struct {
		basis int64
		rule  affiliateRule
		want  int64
	}{
		{1001, affiliateRule{RateBPS: 500}, 50},
		{math.MaxInt64, affiliateRule{RateBPS: 10000}, math.MaxInt64},
		{math.MaxInt64, affiliateRule{RateBPS: 5000}, math.MaxInt64 / 2},
		{1000, affiliateRule{AmountCents: 123}, 123},
		{0, affiliateRule{RateBPS: 10000}, 0},
	} {
		got, err := affiliateCommissionAmount(tc.basis, tc.rule)
		if err != nil || got != tc.want {
			t.Fatalf("basis=%d rule=%+v got=%d want=%d err=%v", tc.basis, tc.rule, got, tc.want, err)
		}
	}
	for _, rule := range []affiliateRule{{RateBPS: -1}, {RateBPS: 10001}, {AmountCents: -1}, {RateBPS: 500, AmountCents: 1}} {
		if _, err := affiliateCommissionAmount(1000, rule); err == nil {
			t.Fatalf("accepted invalid rule %+v", rule)
		}
	}
}

func TestAffiliateLandingPathAndReferrerPrivacy(t *testing.T) {
	for _, path := range []string{"//external.test/", "/%2fexternal.test/", "/\\external.test/", "https://external.test/", "/%0d%0aevil"} {
		if validAffiliateLandingPath(path) {
			t.Fatalf("accepted unsafe landing path %q", path)
		}
	}
	if !validAffiliateLandingPath("/store/product?aff=123") {
		t.Fatal("rejected local product URL")
	}
	if got := affiliateReferrer("https://user:secret@example.test/store?email=private@example.test#secret"); got != "https://example.test/store" {
		t.Fatalf("referrer retained identifiers: %q", got)
	}
	if got := truncateAffiliateText("中文测试", 2); got != "中文" {
		t.Fatalf("invalid UTF-8 truncation: %q", got)
	}
}
