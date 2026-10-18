-- Independent operating cash costs. Customer balance debits are not costs.
CREATE TABLE report_expenses (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 amount_cents bigint NOT NULL CHECK(amount_cents BETWEEN 1 AND 100000000000),
 occurred_on date NOT NULL CHECK(occurred_on>='2000-01-01'),
 category text NOT NULL CHECK(char_length(btrim(category)) BETWEEN 1 AND 80),
 source text NOT NULL CHECK(char_length(btrim(source)) BETWEEN 1 AND 120 AND source=btrim(source)),
 evidence_ref text NOT NULL CHECK(char_length(btrim(evidence_ref)) BETWEEN 1 AND 500 AND evidence_ref=btrim(evidence_ref)),
 note text NOT NULL DEFAULT '' CHECK(char_length(note)<=1000),
 recorded_by bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT report_expenses_source_evidence UNIQUE(source,evidence_ref)
);
CREATE INDEX report_expenses_period ON report_expenses(occurred_on DESC,id DESC);
CREATE TABLE report_expense_voids (
 expense_id bigint PRIMARY KEY REFERENCES report_expenses(id) ON DELETE RESTRICT,
 recorded_by bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 note text NOT NULL CHECK(char_length(btrim(note)) BETWEEN 1 AND 1000),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION protect_report_expense_fact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'report expense facts are append-only' USING ERRCODE='23514';
END $$;
CREATE TRIGGER report_expenses_immutable BEFORE UPDATE OR DELETE ON report_expenses FOR EACH ROW EXECUTE FUNCTION protect_report_expense_fact();
CREATE TRIGGER report_expense_voids_immutable BEFORE UPDATE OR DELETE ON report_expense_voids FOR EACH ROW EXECUTE FUNCTION protect_report_expense_fact();
-- finance.manage is a new capability; roles receive it through explicit RBAC
-- assignment. Existing reports.view never implicitly grants cost mutation.
