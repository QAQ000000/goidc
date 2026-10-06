package store

import "testing"

func TestConfigurationProration(t *testing.T) {
	for _, tc := range []struct{ delta, remaining, duration, want int64 }{
		{1001, 1, 2, 501}, {-1001, 1, 2, -501},
		{285, 15, 30, 143}, {-285, 15, 30, -143},
		{1000, 0, 30, 0}, {1000, 60, 30, 1000},
		{10000000000000000, 2678400, 2678400, 10000000000000000},
	} {
		got, err := proratedConfigurationDelta(tc.delta, tc.remaining, tc.duration)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %d %v", tc, got, err)
		}
	}
	if _, err := proratedConfigurationDelta(100, -1, 30); err == nil {
		t.Fatal("negative remaining accepted")
	}
	if _, err := proratedConfigurationDelta(100, 1, 0); err == nil {
		t.Fatal("zero period accepted")
	}
}
