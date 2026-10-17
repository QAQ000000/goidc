# G09 PostgreSQL read-only resource projection

`internal/g09projection` is a bounded projection adapter for the offline G09
capacity allocator. It is intentionally independent of the application
`store.Store`, runtime lifecycle, DSN/environment configuration, and business
write paths.

The loader accepts an explicitly supplied `*pgxpool.Pool`; it executes exactly
four fixed `SELECT` statements in one `RepeatableRead`, `ReadOnly` transaction.
That gives every resource class the same PostgreSQL snapshot. The narrow
transaction interface used by unit tests exposes only `Query`, `Commit`, and
`Rollback`; production construction goes through the pool adapter.

| Projection | Source and columns | Fixed rows |
| --- | --- | ---: |
| customers | active customer `users.id,balance_cents` | 1000 |
| services | `services.id,user_id` | 2000 |
| invoices | `invoices.id,user_id,amount_cents` | 10000 |
| supplier accounts | active `supplier_accounts.id` | 10 |

Each query has a one-row overflow bound and uses `ORDER BY id`; the loader
rejects short or overflowing results, non-positive/duplicate IDs, negative
balances or invoice amounts, and service/invoice rows owned by a customer not
in the customer projection. Only integer cents are exposed. It never selects
supplier credentials, URLs, names, product bindings, or any other fields.

`UnknownSupplierAccount` is a caller-supplied synthetic ID. It is not queried
from `supplier_accounts` and must not collide with any of the ten real IDs.
This keeps the single unknown-supplier write isolated from real supplier
account capacity.

The result is a detached in-memory projection. It does not prove that a
business operation succeeded, that a worker drained, or that the system passed
a capacity threshold. A successful loader run is therefore projection
readiness evidence only, never a business or capacity PASS.

Tests use mock rows and a fake read-only transaction only; they do not connect
to or mutate a business database. Query, validation, and commit failures roll
back the transaction; success commits after all rows validate.
