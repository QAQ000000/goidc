CREATE TABLE native_configuration_rules (
  id bigserial PRIMARY KEY,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  name text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX native_configuration_rules_product ON native_configuration_rules(product_id,id);

CREATE TABLE native_configuration_predicates (
  id bigserial PRIMARY KEY,
  rule_id bigint NOT NULL REFERENCES native_configuration_rules(id) ON DELETE CASCADE,
  phase text NOT NULL CHECK (phase IN ('condition','result')),
  sort_order integer NOT NULL CHECK (sort_order >= 0),
  group_id bigint NOT NULL REFERENCES product_option_groups(id) ON DELETE RESTRICT,
  option_id bigint REFERENCES product_options(id) ON DELETE RESTRICT,
  operator text NOT NULL CHECK (operator IN ('equals','not_equals','absent','quantity_in','quantity_out')),
  qty_minimum bigint,
  qty_maximum bigint,
  CHECK ((operator='absent' AND option_id IS NULL AND qty_minimum IS NULL AND qty_maximum IS NULL)
    OR (operator IN ('equals','not_equals') AND option_id IS NOT NULL AND qty_minimum IS NULL AND qty_maximum IS NULL)
    OR (operator IN ('quantity_in','quantity_out') AND option_id IS NOT NULL AND qty_minimum IS NOT NULL AND qty_maximum IS NOT NULL AND qty_minimum >= 1 AND qty_maximum >= qty_minimum AND qty_maximum <= 1000000)),
  UNIQUE(rule_id,phase,sort_order)
);
CREATE INDEX native_configuration_predicates_group ON native_configuration_predicates(group_id);
CREATE INDEX native_configuration_predicates_option ON native_configuration_predicates(option_id) WHERE option_id IS NOT NULL;
