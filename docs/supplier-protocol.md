# Supplier protocol: 魔方财务 legacy v1

新增提供方按[供应商模块开发契约](architecture/supplier-module-development.md)接入。迁移 166 为专用供应商账户增加完整普通配置、schema 和逐字段命名凭据；旧魔方 API 与密文格式保留。账户身份及普通配置不可重定向，凭据仅在没有进行中或待核验任务时轮换。列表的配置状态只表示本地检查，不代表真实连接成功。待派发任务使用派发时账户配置，未知写结果保留原账户租约。

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

Non-editable automatic hostname/password rules are supported when their length,
character groups and optional hostname prefix are recognized. They use secure
random generation and participate in the revision; interactive credential input
remains unsupported. Products without automatic rules retain the previous hash.

`Providers()` returns registry metadata for the admin UI. Import and checkout
both call `ValidateSelection` to require every fixed upstream choice and
mandatory field, reject unknown IDs, and calculate base, option, and setup
costs in cents. Dropdown and radio options are supported. A DCIM fixed
specification whose provider-specific `option_type` has no variable range is
normalized to a regular choice. A single-range disk quantity is also supported
when it has one choice, no linkage, and a non-staged unit price; the submitted
integer is checked against the provider range and multiplies the option and
setup price. Arbitrary interactive quantities, staged pricing, OS linkage and
advanced linkage remain unsupported. Text and dropdown custom fields are
supported only without a supplier regex rule. The store seals custom field
values and never projects them back in binding responses.

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

Creation may return only the invoice ID: its single host item recovers the
resource ID, followed by a resource/product check before spending balance.
Scalar and single-element array IDs are accepted at either response location;
multiple or contradictory resources are rejected. `apply_credit` uses `enough=1`.
The exact invoice is read again after payment, including an incomplete or failed
payment response, and must be Paid with unchanged cost and resource. No payment
is resent when that read cannot confirm the outcome. Pending resources proceed
through the existing sync queue. See [real unattended acceptance](archive/README.md#f-1fa4fed8401c)
for the 2026-10-13 CDN experience product result and deployment boundary.

On 2026-10-19, the current API/store/worker path passed another isolated PG18
acceptance against the real CDN experience product 391. Resource 4089 was
created and renewed once; original invoices 811026 and 811027 were Paid with
one resource-owned host/renew line respectively. Each operation cost one CNY
cent, the account balance decreased by two cents, and the confirmed due date
advanced from November 19 to December 19. Polling, manual suspend/terminate
target rejection while the resource remained Active, and explicit not-applied
closure passed without false lifecycle notifications or inventory release.
The isolated database was destroyed and the upstream resource retained. This
does not establish resident automatic renewal, successful real lifecycle
changes, public callbacks or deployment. Sanitized evidence remains under
the ignored `output/supplier-real/current/` directory.

`Sync` first uses GET `/host/header?host_id=...&source=API`, which is the
endpoint used by the legacy PHP implementation, and falls back to
`/cart/hostinfo?hostid[0]=...` for older upstream installations.
`CreateResult.Status` is `active` only after an upstream `Active` status is
observed. Other successful outcomes remain `provisioning`. `Execute` supports
`sync`; pause, resume and terminate remain unsupported. Renewal uses the optional
`RenewalAdapter` workflow described below. Refund requests
and resource deletion are handled at the remote supplier. A pending refund or
cancellation request alone must not terminate the local service. The worker
periodically reads the actual upstream resource status and re-arms completed
sync jobs after five minutes. An observed `Deleted`, `Terminated` or `Cancelled`
state maps to local `terminated` and releases inventory once. Already terminated
services are excluded from further periodic scheduling. This state synchronization
does not refund the local invoice or credit the local customer automatically.
No local termination command or `/host/cancel` request is sent. The
2026-10-13 `Deleted` mapping is covered by isolated database tests and has not
yet been deployed to the resident Go processes.

Following the 2026-10-13 legacy review, the working tree now uses the maintained
PHP project's lifecycle merge rules in the shared application path. Local
`cancelled`/`terminated` remain terminal; `active`/`suspended` cannot regress to
provisioning; upstream `Active` cannot release a local suspension. This applies
to callbacks and polling under a service row lock. Only an explicit, verified
`resume` job can accept suspended→active; ordinary sync never does.
Accepted `Active` clears the suspension reason; accepted `Suspended` preserves
its type prefix without duplication. Ignored states do not overwrite the local
suspension reason. A stale `Pending` response only schedules a follow-up when
the resulting local state is still provisioning. `Fraud` remains unsupported
and requires explicit reconciliation rather than an inferred deletion.
Isolated supplier race tests cover delayed callbacks and an in-flight `Active`
read completing after a deletion callback. These changes are not yet deployed.
See [legacy lifecycle review](archive/README.md#f-6836dc177d91).

## Automatic renewal admission and payment

Automatic renewal uses the local 24-hour due window and then reads the actual
upstream renewal quotation. After rechecking the service version and original
account/binding identity, an upstream deadline beyond the database's current
24-hour window causes a safe skip: only advance the local due date, clear the
old automatic-renewal error and record `supplier.auto_renewal.skipped` with
`upstream_not_due`. Do not create an invoice, spend balance or count the skip
as a paid renewal. Manual advance renewal remains available.

Migration 171 records an immutable `auto_renewal_invoice_sources` fact in the
same transaction as each automatic invoice: invoice, service, customer,
expected due date and renewal generation. Insert validation requires the
original unpaid invoice and its single matching renewal line. Historical
invoices are not backfilled from an `auto-renew-*` key, because a customer can
choose the same key for a manual request.

`PayAutoRenewalInvoice` uses the existing balance transaction and repeats the
source, ownership, single-line association, switch, original due date,
generation, service-state and cancellation checks before debit. Supplier
payments also hold the original account's shared lock and recheck its active
state, version, capability and quotation identity/cost ceiling before local
collection. A disabled or changed account leaves the original invoice unpaid.
Ordinary
manual and credit payment keep their existing rules. An unpaid invoice left
by insufficient balance, changed intent or a process interruption is retained
for manual payment; a later scheduler pass or top-up does not silently collect
it. A paid invoice whose supplier renewal remains unresolved also requires
operator handling; replaying that receipt must not count as another successful
automatic collection or clear its pending error. An interrupted full daily
pass may resume scanning other candidates.

`invoice.paid` confirms local collection. `service.renewed` is emitted once
only after the full upstream period is confirmed. An unknown renewal write
keeps the original account lease and request identity, stops automatic resend
and requires the existing read-only reconciliation path. A receipt lost
before the invoice checkpoint must not be guessed from an unrelated invoice.

Release migration 171 together with the matching API and worker after stopping
old writers. Keep the original migration checksums and financial history;
mixed old/new automatic writers are unsupported. Independent PostgreSQL and
fake-provider fault tests establish these code boundaries; actual due-period
purchase, public callbacks and production rollout require separate evidence.

The real resource 4089 passed a zero-write negative acceptance with four
current worker processes, TERM/restart and a real 65-second minute tick. A
stale local deadline was advanced to the unchanged upstream December 19
deadline, with exactly one skip audit and daily full pass, zero invoices,
renewal jobs, debits, credit payments or renewal notifications. The remote
balance remained CNY 9.96 before and after. The guarded disposable schema was
dropped and the resource retained. This proves early-renewal prevention and
resident scheduling against that provider; a genuinely due automatic purchase
remains unverified. Sanitized evidence is in `output/supplier-autorenew/`.

The downstream exposes the legacy callback `POST /api/host/sync` (and the
versioned alias `/api/v1/host/sync`). The upstream sends `id` as the local
service ID and `host_id` as its resource ID, together with `domainstatus`,
`domain`, `username`, `password`, IPs, port, OS and `nextduedate`. The callback
signature is the uppercase MD5 of compact, lexicographically sorted JSON with
exactly `id`, `rand_str` and the per-service token. It validates the supplier
binding and resource identity in a transaction, then stores credentials using
the existing service encryption key. It does not use browser sessions or an
Origin header.

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

Missing compiled providers remain visible in the application catalogue and
supplier account list. An administrator can stop an existing active account
with `PUT /api/v1/admin/suppliers/{id}` and
`{"disable_only":true,"active":false,"expected_version":<current version>}`.
This narrow operation changes only `active`, `version` and `updated_at`; it
preserves the name, provider, endpoint, username and credential ciphertext.
It neither constructs an adapter nor decrypts credentials, so an absent
implementation or encryption key does not prevent a safe stop. Other account
fields cannot be supplied in the same request. New accounts, full edits and
re-enabling still require a supported compiled provider.

The stop rechecks the current session and `products.view` / `products.manage`
grant inside the catalogue transaction, requires an active account and matching
version, and retains the `running_job_id` guard. An in-flight or unresolved
delivery must be reconciled before stopping the account. The
`supplier.disabled` audit event commits with the state change; audit failure,
stale versions and permission/session revocation leave the account unchanged.
The supplier page exposes this operation for missing providers and disables
their normal editor and catalogue fetch. Isolated PG18 regression entry:
`REQUIRE_INTEGRATION=1 ./scripts/go.sh test ./internal/httpapi -run '^TestMissingSupplierDisable' -count=1`.

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
to terminated first. Purchase-invoice refunds retain that restriction. Renewal
invoice refunds are allowed before dispatch or after a known failed/explicitly
reconciled renewal; they cancel the job without shortening the service period.
Unknown or successful upstream renewals cannot be reversed solely in local finance.

Crash recovery reads only durable row state, because process memory and the
upstream response are gone. `sync` is the only read-only action in the worker
vocabulary, so a crashed or failed sync has no unknown upstream write to
reconcile: stale recovery releases its account lease, clears the dispatch marker
and retries it rather than routing it to manual verification. Every other action
keeps the conservative contract: it enters `waiting_verification`, retains the
account lease and is never resent automatically. Automated overdue suspension
resolves the immutable service binding and compiled provider capability. Supported
lifecycle actions use the existing durable dispatch/account lease; unsupported
supplier actions create visible `waiting_manual` work without a dispatch marker,
account lease or premature status/notification change. Upgrades remain unsupported
for the default provider. A remote refund request alone does not unlock local
refunds; actual resource termination must first be confirmed.

### Lifecycle execution and manual reconciliation

Administrators with `services.view` / `services.manage` and customer scope may
queue `suspend`, `resume` and `terminate`. The API and worker share capability
mapping, and dispatch rechecks the configured runtime adapter. The default
`zjmf_finance_v1` still declares none of these write capabilities: its operator
performs the action upstream, then uses
`POST /api/v1/admin/services/{id}/supplier-lifecycle/verify`. Customer lifecycle
and cancellation requests remain unsupported.

An untouched manual task records `manual_lifecycle:true`, the original resource,
job ID and attempts (normally zero). Verification reads that fixed resource,
requires the original product and exact requested target: Suspended for suspend,
Active for resume, or Deleted/Terminated/Cancelled for terminate. A missing host
is insufficient evidence of termination. Permissions, customer scope, live
session, service version/snapshot, account version and task identity are checked
before and after the read. This read takes no persistent account lease, so a
crash can be retried safely. Conflicting work remains blocked. Completion and
audit commit atomically; identical receipts replay safely and are hidden from
customer job results. `not_applied` requires an explicit acknowledgement and
note, closes the task as failed and preserves the resource state.

A sent write with an uncertain result remains `waiting_verification`, keeps the
account lease and uses the existing verification endpoint. It is never resent
automatically. Reconciliation also requires the exact target status. Only a
positively confirmed termination releases inventory, once. Pending/manual
requests produce no false lifecycle notification.

### Billing suspension and paid recovery

The bounded scheduler queues overdue suspensions after the existing 24-hour
grace period. Unsupported or locally misconfigured supplier bindings become
manual tasks; old skip audits do not hide them. Invoice locks precede service
locks; duplicate invoices, concurrent scans and audit failures retain the
in-flight/idempotency/transaction boundaries.

A successful system suspension carries immutable `system_overdue_suspension`
and original invoice identity, plus a service marker referencing that exact job.
Only this latest successful lifecycle action permits automatic recovery after
the original bill is paid and no other qualifying overdue debt remains. Any
explicit non-system lifecycle change, or newly observed upstream suspension
reason, invalidates the marker. API callers cannot use reserved scheduler key
prefixes. Historical successful pauses lacking this provenance require manual
handling and are not guessed or backfilled.

Recovery is one task per source suspension: supported Resume executes normally;
otherwise it is an explicit manual resume task. Automatic dispatch rechecks payment/source eligibility and cancels obsolete
unsent automatic jobs. A manual task represents operator responsibility: no
platform dispatch marker can prove that a person has not already acted upstream.
Billing changes therefore never discard a manual result. The operator verifies
the actual target or explicitly acknowledges that the action was not applied.
A confirmed late suspension can enter paid recovery; a confirmed late recovery
with new debt can be suspended again by the next scan. Unknown/dispatched work
retains its original verification boundary. No local credit, refund or service
extension is inferred from these operations.

Migration 170 preserves old requests and adds credential-free payload checks
and immutable lifecycle action/service/key/source identity. Stop old writers
before applying the migration and deploying matching API/worker binaries; direct
old-binary downgrade on the new schema is unsupported. Isolated tests and local
browser fixtures do not establish real provider execution or production rollout.

Use a dedicated upstream customer account for automated orders: this lease
coordinates local workers, not people or other deployments sharing that account.
Before external acceptance, set `PUBLIC_ORIGIN` to the real downstream URL
reachable by the supplier; the development value `http://localhost:3088` is
only a local preview address. The supplier must be able to POST the callback
from its network, and the API process must be restarted after changing the
environment.

To add another system, implement `Adapter` and register its factory and metadata
in `internal/supplier/registry.go`. Normalize catalog/configuration/money/state
and sanitized errors inside that adapter. The UI reads provider metadata from
`GET /api/v1/admin/supplier-providers`; business code has no magic provider-name
switch. IDs currently use positive numeric handles. A provider with opaque/UUID
IDs needs a durable provider-to-handle mapping rather than lossy parsing or
hashing. Additional credentials use the account's explicit named format and
per-field AAD; the current first provider retains username/password aliases.

A new lifecycle capability must implement and test `Execute` with normalized
resource/product identity and a positive target status. Advertising a flag alone
cannot bypass runtime capability, permissions, account lease or reconciliation.
Future interactive quantities, staged pricing and other configurations likewise
require normalized schema support and checkout validation. Protocol fixtures and isolated databases prove
the local contracts; a real supplier account remains required for external
acceptance.


## Legacy renewal integration (2026-10-13)

Schema136 enables `renew` jobs and immutable supplier quotation snapshots on
renewal invoice items. `RenewalAdapter.QuoteRenewal` reads `/host/header` and
`/host/renewpage?hostid=...&billingcycles=...`; it checks resource/product identity,
Active/Suspended state, CNY, matching cycle/due date and the imported cost cap.
Current monthly, quarterly and annually cycles are supported; once is rejected.
The price is the existing resource renewal amount, including its fixed options,
not a new-product catalogue price. A local retail renewal invoice snapshots the
supplier quote separately from the customer's retail price and benefit terms.

Local payment queues exactly one `supplier-renew-{invoiceID}` job and consumes
the existing retail renewal benefit once. It never extends a supplier service's
local due date. Manual and automatic renewal share this settlement path. An
unresolved paid renewal blocks another local renewal invoice even when its job
has failed; finance must resolve that original invoice first.

The worker holds the existing account lease, checks the local invoice is paid,
resource binding and account version, then rechecks the quotation. Only then
it sends `POST /host/renew` with `hostid` and `billingcycles`. The response accepts
`invoiceid` or `data.invoice_id`; the exact invoice ID is durably checkpointed
before any balance payment. Invoice items must contain exactly one `renew` item
and only resource-owned discounts/promotions. Currency, quote cost and cost cap
are checked again. It sends `/apply_credit` once, with `use_credit=1,enough=1`;
credit-limit fallback and automatic replay of sent writes are disabled.

The same invoice must be Paid and the same resource must report at least one
complete purchased calendar period beyond the quoted due date. Incomplete or
failed payment responses are resolved by reading that invoice, without another
charge. Unknown results retain the lease and enter `waiting_verification`; known
rejections or cost/ownership mismatches fail explicitly and release the lease.
Manual successful reconciliation also confirms the original invoice and extended
due date, rather than accepting an already-active resource as proof of renewal.
Late polling/callback responses cannot shorten a confirmed supplier due date.
See [renewal acceptance and runbook](archive/README.md#f-f2eac220df6f).
