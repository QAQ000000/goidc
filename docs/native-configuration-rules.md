# Native configuration rules

Native rules constrain a complete product option selection. They use the current product's stable group and option IDs, independent of imported PHP identities. Each rule has a name, enabled flag, ordered conditions, and ordered results. All conditions must match before the rule applies; then every result must match. Rules do not add or remove selections automatically. A nonmatching result rejects the selection with the rule name.

Operators:

| Operator | Match |
| --- | --- |
| `equals` | The referenced option is selected. |
| `not_equals` | The group has a selected option other than the referenced option. An absent group does not match. |
| `absent` | No option from the group is selected. It has no option ID. |
| `quantity_in` | The referenced quantity option is selected and its quantity is inside the inclusive `[qty_minimum, qty_maximum]` interval. |
| `quantity_out` | The referenced quantity option is selected and its quantity is outside that inclusive interval. An absent or different option does not match. |

Optional groups can be absent. Required groups and hierarchy remain governed by the normal product validator. Quantity predicates are accepted only for quantity-type groups. The range is 1 to 1,000,000. Each product supports at most 100 rules, with 1 to 20 conditions and 1 to 20 results per rule. Names are at most 120 characters. Saving rejects foreign, inactive, malformed, or type-incompatible references. Deleting a referenced group or option requires first editing or deleting every referencing rule, including disabled rules.

Admin routes require `products.view` and `products.manage`, an active account, and the product's current `expected_revision`:

| Method | Route | Body |
| --- | --- | --- |
| `POST` | `/api/v1/admin/products/{id}/configuration-rules` | Rule fields and `expected_revision` |
| `PUT` | `/api/v1/admin/products/{id}/configuration-rules/{rule_id}` | Full replacement and `expected_revision` |
| `DELETE` | `/api/v1/admin/products/{id}/configuration-rules/{rule_id}` | `expected_revision` |

The product revision, rule change, and audit event commit atomically under the catalog lock. The admin product projection includes enabled and disabled rules. Public catalog and upgrade product projections contain enabled rules only. The same store validator handles cart writes, checkout, promotions, and service configuration and product quotes. Product duplication copies rules and remaps every group and option reference in its transaction.
