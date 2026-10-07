ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check
 CHECK(action IN (
   'create','suspend','resume','terminate','sync','upgrade','power_on','power_off','reboot','force_off','force_reboot','reset_password','reinstall',
   'snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore',
   'nat_acl_create','nat_acl_delete','nat_web_create','nat_web_delete','cdrom_mount','cdrom_unmount','rescue_system','exit_rescue_system'));
