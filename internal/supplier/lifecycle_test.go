package supplier

import "testing"

func TestSupplierLifecycleCapabilitiesAndTargetProof(t *testing.T) {
	caps := Capabilities{Sync: true, Suspend: true}
	if !caps.SupportsAction("suspend") || !caps.SupportsAction("sync") || caps.SupportsAction("resume") || caps.SupportsAction("terminate") || caps.SupportsAction("arbitrary_execute") {
		t.Fatal("lifecycle admission must respect independent capabilities and reject unknown actions")
	}
	for _, tc := range []struct {
		action, status string
		want           bool
	}{
		{"suspend", "Suspended", true}, {"suspend", "Active", false},
		{"resume", "active", true}, {"resume", "Suspended", false},
		{"terminate", "Deleted", true}, {"terminate", "Cancelled", true},
		{"terminate", "Terminated", true}, {"terminate", "", false},
		{"terminate", "Pending", false}, {"terminate", "Fraud", false},
		{"sync", "Deleted", false}, {"unknown", "active", false},
	} {
		if got := LifecycleTargetConfirmed(tc.action, tc.status); got != tc.want {
			t.Errorf("%s/%s confirmed=%v, want %v", tc.action, tc.status, got, tc.want)
		}
	}
}
