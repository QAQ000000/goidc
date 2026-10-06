ALTER TABLE products ADD COLUMN approval_required boolean NOT NULL DEFAULT false;
ALTER TABLE orders ADD COLUMN review_status text NOT NULL DEFAULT 'approved'
  CHECK (review_status IN ('pending','approved','rejected'));
ALTER TABLE orders ADD COLUMN notes text NOT NULL DEFAULT '';
CREATE INDEX orders_review_status ON orders(review_status,id DESC);
