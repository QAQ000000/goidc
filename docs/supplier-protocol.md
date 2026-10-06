# Supplier protocol: 魔方财务 legacy v1

`internal/supplier` exposes `Adapter` and `New(Account)`. The provider key is
`zjmf_finance_v1`. Product identity, revision, price and upstream configuration
remain server-side. The caller must hold a durable, per-account lease across
`Create` and freeze that account after any `Error.Unknown` until an operator
reconciles the upstream invoice and host IDs. This package never retries a
write after it has been sent.

The legacy PHP implementation uses POST form login at `/zjmf_api_login` and a
Bearer JWT. Catalog is GET `/cart/all`; product detail is GET
`/cart/get_product_config?pid=...`. Product detail carries `products`,
`product_pricings`, `config_groups`, `customfields`, and `advanced`. Products
must have a CNY price table and a supported configuration schema to be
importable. Products with complex configuration are returned with
`Supported=false` and `UnsupportedReason`; they cannot be purchased through
this adapter. Prices are parsed as decimal strings into integer cents. The
local `once` cycle maps to the legacy `onetime` key and its `osetupfee`.
Product revision hashes the normalized identity, supplier version, prices,
options, and fields; changing stock alone does not invalidate a binding.

`Providers()` returns registry metadata for the admin UI. Import and checkout
both call `ValidateSelection` to require every fixed upstream choice and
mandatory field, reject unknown IDs, and calculate base, option, and setup
costs in cents. Dropdown and radio options are supported; quantity, staged
pricing, OS and advanced linkage remain unsupported. Text and dropdown custom
fields are supported only without a supplier regex rule. The store seals custom
field values and never projects them back in binding responses.

Product payment rules accept the legacy JSON string or object form. Recurring
products expose recurring cycles only; one-time products expose `once` only.
Hidden/retired products, missing payment rules, malformed setup prices and
products that require unsupported hostname/password rules cannot be imported.

`Create` checks the selected product's revision, cycle and maximum cost. It
reads currency from the top-level `user.currency` in GET `/user_info` and sends
the upstream product's `location_version` as `supplier_version`. It
uses the stable downstream URL/token/local service ID at POST `/cart/clear`,
then POST `/cart/add_to_shop`, POST `/cart/settle`, GET
`/get_invoices_detail?id=...`, and POST `/apply_credit`. The invoice detail must
identify the returned invoice, use CNY, and have a subtotal within the caller's
maximum before credit is applied. A host invoice item must match the returned
upstream host ID. There must be exactly one host line; setup, discount and promo
lines are accepted only for that same resource. Unrelated invoice lines are
rejected. Credit-limit fallback is not used. Only
`Unpaid` invoices are sent to `/apply_credit`; `Paid` invoices are read
without a second payment attempt. Other invoice states are rejected. Existing
host/invoice IDs from `cart/clear` are returned for recovery. An existing host
without an invoice is an unknown outcome; it is never reported as a new
zero-cost success. An uncertain response after cart mutation returns
`Error.Unknown=true` and known IDs.

`Sync` uses GET `/cart/hostinfo?hostid[0]=...`. `CreateResult.Status` is
`active` only after an upstream `Active` status is observed. Other successful
outcomes remain `provisioning`. The `Execute` action `sync` is supported;
pause, resume, terminate and renew are unsupported. Legacy termination uses a
separate cancellation path whose effect has not been established here.

Supplier responses and transport errors never enter returned error strings.
The HTTP client verifies TLS, disables redirects and environment proxies,
limits each response to 2 MiB, and times out after 20 seconds. Plain HTTP is
allowed for loopback or literal private IP supplier endpoints.

## Business integration and future providers

Schema081 separates `supplier_accounts`, immutable `supplier_product_bindings`
and service delivery snapshots from local retail products. The admin selects
fixed upstream options/fields and a cost ceiling, then imports an inactive local
product. Retail prices, description and stock can be maintained locally. Source,
account identity, fixed configuration and cycle cannot be retargeted; reimport a
new product when changing those fields. Checkout uses the normal local cart and
atomically records the binding. Only the worker uses the upstream shared cart.

AES-GCM binds credentials and fixed custom fields to their owner and domain.
The existing `PROVISION_ENCRYPTION_KEY` must remain stable. No supplier raw
response or password enters customer DTOs, audit payloads or job result JSON.

The worker holds `supplier_accounts.running_job_id` durably before any network
write. Waiting for another account job does not consume a delivery attempt.
Unknown results retain this lease and enter `waiting_verification`; subsequent
jobs wait and no write is automatically retried. A resource collision also
enters verification immediately. The operator confirms an actual upstream
resource, or explicitly acknowledges the absence of both resources and invoices
with a note. A synchronized service cannot be attached to a different resource.
Due dates come from the upstream response. Pending provisioning gets delayed
sync jobs, with a bounded limit of120 automatic syncs; thereafter an admin can
request sync. Active supplier services cannot be refunded while their upstream
resource remains active; cancellation must be performed upstream and synchronized
to terminated first. Automatic renewal, upgrades and lifecycle writes remain
blocked for this provider.

Use a dedicated upstream customer account for automated orders: this lease
coordinates local workers, not people or other deployments sharing that account.
Before external acceptance, set `PUBLIC_ORIGIN` to the real downstream URL;
the development value `http://localhost:3088` is only a local preview address.

To add another system, implement `Adapter` and register its factory and metadata
in `internal/supplier/registry.go`. Normalize catalog/configuration/money/state
and sanitized errors inside that adapter. The UI reads provider metadata from
`GET /api/v1/admin/supplier-providers`; business code has no magic provider-name
switch. IDs currently use positive numeric handles. A provider with opaque/UUID
IDs needs a durable provider-to-handle mapping rather than lossy parsing or
hashing. Additional credentials require a versioned encrypted account payload;
the current first provider uses username/password.

Capabilities reserve suspend/resume/terminate/renew, but advertising a flag alone
does not enable those workflows. Implement and verify the matching store,
queue, finance and UI contracts before opening a new lifecycle capability.
Future interactive/quantity configurations likewise require normalized schema
support and checkout validation. Protocol fixtures and isolated databases prove
the local contracts; a real supplier account remains required for external
acceptance.
