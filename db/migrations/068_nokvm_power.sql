ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check
  CHECK(action IN ('create','suspend','resume','terminate','sync','upgrade',
    'power_on','power_off','reboot','force_off','force_reboot'));
