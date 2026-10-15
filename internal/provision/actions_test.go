package provision

import "testing"

func TestIsReadOnlyActionOnlyAllowsSync(t *testing.T) {
	if !IsReadOnlyAction("sync") {
		t.Fatal("sync must be retryable after a crash")
	}
	// Every other action in the provision_jobs vocabulary can modify upstream
	// state or depends on an unknown command result, so crash recovery must keep
	// the conservative manual-verification contract.
	for _, action := range []string{
		"create", "renew", "suspend", "resume", "terminate", "upgrade",
		"power_on", "power_off", "reboot", "force_off", "force_reboot",
		"reset_password", "reinstall", "rescue_system", "exit_rescue_system",
		"snapshot_create", "snapshot_delete", "snapshot_restore",
		"backup_create", "backup_delete", "backup_restore",
		"nat_acl_create", "nat_acl_delete", "nat_web_create", "nat_web_delete",
		"cdrom_mount", "cdrom_unmount",
	} {
		if IsReadOnlyAction(action) {
			t.Fatalf("%s must not be treated as read-only", action)
		}
	}
	for _, action := range []string{"", "SYNC", "sync ", "unknown"} {
		if IsReadOnlyAction(action) {
			t.Fatalf("unexpected read-only action %q", action)
		}
	}
	got := ReadOnlyActions()
	if len(got) != 1 || got[0] != "sync" {
		t.Fatalf("read-only vocabulary changed: %v", got)
	}
	got[0] = "mutated"
	if ReadOnlyActions()[0] != "sync" {
		t.Fatal("ReadOnlyActions must return a copy")
	}
}
