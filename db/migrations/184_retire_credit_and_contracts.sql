-- Stop old API/worker and other writers before deploying this retirement.
-- Keep account limits, debts, receipts, contract snapshots and audit history.
UPDATE credit_settings SET enabled=false,auto_recharge=false,version=version+1,updated_at=now() WHERE id=1;
UPDATE contract_settings SET enabled=false,version=version+1,updated_at=now() WHERE id=1;
UPDATE site_settings SET admin_settings=jsonb_set(admin_settings,'{frontend_credit_enabled}','false'::jsonb),version=version+1,updated_at=now() WHERE id=1;
