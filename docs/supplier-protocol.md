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

`Create` checks the selected product's revision, cycle and maximum cost. It
reads currency from the top-level `user.currency` in GET `/user_info` and sends
the upstream product's `location_version` as `supplier_version`. It
uses the stable downstream URL/token/local service ID at POST `/cart/clear`,
then POST `/cart/add_to_shop`, POST `/cart/settle`, GET
`/get_invoices_detail?id=...`, and POST `/apply_credit`. The invoice detail must
identify the returned invoice, use CNY, and have a subtotal within the caller's
maximum before credit is applied. A host invoice item must match the returned
upstream host ID. Credit-limit fallback is not used. Only
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
