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
`product_pricings`, `config_groups`, `customfields`, and `advanced`. Only a
single CNY price table without configuration or custom fields is currently
importable. Products with complex configuration are returned with
`Supported=false` and `UnsupportedReason`; they cannot be purchased through
this adapter. Prices are parsed as decimal strings into integer cents.

`Create` checks the selected product's revision, cycle and maximum cost. It
uses the stable downstream URL/token/local service ID at POST `/cart/clear`,
then POST `/cart/add_to_shop`, POST `/cart/settle`, GET
`/get_invoices_detail?id=...`, and POST `/apply_credit`. The invoice detail must
identify the returned invoice, use CNY, and have a subtotal within the caller's
maximum before credit is applied. Credit-limit fallback is not used. Existing
host/invoice IDs from `cart/clear` are returned for recovery. An uncertain
response after cart mutation returns `Error.Unknown=true` and known IDs.

`Sync` uses GET `/cart/hostinfo?hostid[0]=...`. `CreateResult.Status` is
`active` only after an upstream `Active` status is observed. Other successful
outcomes remain `provisioning`. The `Execute` action `sync` is supported;
pause, resume, terminate and renew are unsupported. Legacy termination uses a
separate cancellation path whose effect has not been established here.

Supplier responses and transport errors never enter returned error strings.
The HTTP client verifies TLS, disables redirects and environment proxies,
limits each response to 2 MiB, and times out after 20 seconds. Plain HTTP is
allowed only for loopback integration tests.
