-- Keep historical unowned ticket_created jobs unlinked; their text is not identity proof.
ALTER TABLE email_outbox ADD COLUMN ticket_id bigint REFERENCES tickets(id);
CREATE INDEX email_outbox_ticket ON email_outbox(ticket_id) WHERE ticket_id IS NOT NULL;
