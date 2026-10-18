-- Persist the identity at the time of public writing. Current account roles
-- cannot establish whether an old message was a staff response.
ALTER TABLE ticket_replies ADD COLUMN author_kind text NOT NULL DEFAULT 'unknown' CHECK(author_kind IN ('unknown','staff','customer'));
ALTER TABLE tickets ADD COLUMN initial_author_kind text NOT NULL DEFAULT 'unknown' CHECK(initial_author_kind IN ('unknown','staff','customer'));
UPDATE tickets SET initial_author_kind='staff' WHERE created_by IS NOT NULL;
UPDATE ticket_replies r SET author_kind=CASE e.kind WHEN 'staff_replied' THEN 'staff' ELSE 'customer' END
FROM ticket_notification_events e WHERE e.reply_id=r.id AND e.actor_id=r.author_id AND e.kind IN ('staff_replied','customer_replied');
UPDATE ticket_replies r SET author_kind=CASE WHEN coalesce(s.payload->>'admin_id','') ~ '^[1-9][0-9]*$' THEN 'staff' WHEN coalesce(s.payload->>'uid','') ~ '^[1-9][0-9]*$' THEN 'customer' ELSE 'unknown' END
FROM legacy_source_records s WHERE s.entity='ticket_replies' AND s.source=r.legacy_source AND s.legacy_id=r.legacy_id;
UPDATE tickets t SET created_by=m.target_id,initial_author_kind='staff'
FROM legacy_source_records s JOIN legacy_id_map m ON m.source=s.source||':staff' AND m.entity='staff' AND m.legacy_id=CASE WHEN coalesce(s.payload->>'admin_id','') ~ '^[1-9][0-9]*$' THEN (s.payload->>'admin_id')::bigint END
WHERE s.entity='tickets' AND s.source=t.legacy_source AND s.legacy_id=t.legacy_id;
CREATE TABLE ticket_ratings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 reply_id bigint UNIQUE REFERENCES ticket_replies(id),
 initial_ticket_id bigint UNIQUE REFERENCES tickets(id),
 customer_id bigint NOT NULL REFERENCES users(id),
 staff_id bigint NOT NULL REFERENCES users(id),
 stars smallint NOT NULL CHECK(stars BETWEEN 1 AND 5),
 provenance text NOT NULL DEFAULT 'native' CHECK(provenance IN ('native','legacy')),
 rated_at timestamptz,
 recorded_at timestamptz NOT NULL DEFAULT now(),
 CHECK((reply_id IS NULL)<>(initial_ticket_id IS NULL)),
 CHECK(provenance='legacy' OR rated_at IS NOT NULL)
);
CREATE INDEX ticket_ratings_staff ON ticket_ratings(staff_id,id);
-- Source stars are historical facts. The source has no rating timestamp, so
-- rated_at stays NULL rather than using an import time as a customer action.
INSERT INTO ticket_ratings(reply_id,customer_id,staff_id,stars,provenance)
SELECT r.id,t.user_id,r.author_id,(s.payload->>'star')::smallint,'legacy'
FROM ticket_replies r JOIN tickets t ON t.id=r.ticket_id JOIN legacy_source_records s ON s.source=r.legacy_source AND s.entity='ticket_replies' AND s.legacy_id=r.legacy_id
WHERE r.author_kind='staff' AND coalesce(s.payload->>'star','') ~ '^[1-5]$';
