package provision

import "slices"

// readOnlyActions lists the provision_jobs.action values that only read upstream
// state and cannot have an unknown write result to reconcile. provision_jobs
// accepts exactly create, renew, suspend, resume, terminate, sync, upgrade,
// power_on, power_off, reboot, force_off, force_reboot, reset_password,
// reinstall, snapshot_create, snapshot_delete, snapshot_restore, backup_create,
// backup_delete, backup_restore, nat_acl_create, nat_acl_delete, nat_web_create,
// nat_web_delete, cdrom_mount, cdrom_unmount, rescue_system and
// exit_rescue_system (migration 136). Both the NOKVM adapter and the supplier
// adapter implement only "sync" as a pure read, so it is the complete set.
// Extending this list is a safety decision: it tells crash recovery that the
// action can be retried automatically after a worker dies, so only add an action
// whose adapter provably never writes upstream.
var readOnlyActions = []string{"sync"}

// IsReadOnlyAction reports whether an action may be retried after a crash
// without manual verification. Every other action keeps the conservative
// contract: an unknown upstream outcome releases nothing and is never resent
// automatically.
func IsReadOnlyAction(action string) bool {
	return slices.Contains(readOnlyActions, action)
}

// ReadOnlyActions returns a copy of the read-only vocabulary for SQL filters.
func ReadOnlyActions() []string {
	return slices.Clone(readOnlyActions)
}
