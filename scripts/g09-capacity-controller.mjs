import path from 'node:path';
import { createG09CapacityAllocator } from './g09-capacity-allocator.mjs';
import { validateG09CapacityProfile, g09CapacityLoadProfile } from './g09-capacity-contract.mjs';
import { runG09Load, validateG09Profile } from './g09-load-lib.mjs';

const fail = code => { throw new Error(code); };
const ownKeys = value => Object.keys(value).sort();
const requiredProjectionKeys = ['customers', 'invoices', 'services', 'supplierAccounts', 'unknownSupplierAccount'];

function validateProjection(resources) {
  if (!resources || typeof resources !== 'object' || Array.isArray(resources)) fail('g09_controller_projection_invalid');
  if (ownKeys(resources).sort().join(',') !== requiredProjectionKeys.join(',')) fail('g09_controller_projection_shape_invalid');
  if (!['customers', 'invoices', 'services', 'supplierAccounts'].every(key => Array.isArray(resources[key])) ||
      !resources.unknownSupplierAccount || typeof resources.unknownSupplierAccount !== 'object' || Array.isArray(resources.unknownSupplierAccount)) fail('g09_controller_projection_invalid');
  // The projection is the only resource boundary. It is copied by the allocator
  // and never interpreted as a database handle or query source.
  return Object.freeze({
    customers: Object.freeze(resources.customers.map(row => Object.freeze({ ...row }))),
    invoices: Object.freeze(resources.invoices.map(row => Object.freeze({ ...row }))),
    services: Object.freeze(resources.services.map(row => Object.freeze({ ...row }))),
    supplierAccounts: Object.freeze(resources.supplierAccounts.map(row => Object.freeze({ ...row }))),
    unknownSupplierAccount: Object.freeze({ ...resources.unknownSupplierAccount }),
  });
}

function validateRunRoot(runRoot) {
  if (typeof runRoot !== 'string' || !path.isAbsolute(runRoot) || !(runRoot.startsWith('/tmp/') || runRoot.startsWith('/dev/shm/'))) fail('g09_controller_run_root_invalid');
  return runRoot;
}

function validateLifecycle(lifecycle) {
  if (!lifecycle || typeof lifecycle !== 'object' ||
      !['start', 'stop', 'drain'].every(name => typeof lifecycle[name] === 'function')) fail('g09_controller_lifecycle_invalid');
  return lifecycle;
}

function validateObserver(observer) {
  if (!observer || typeof observer !== 'object' ||
      !['start', 'snapshot', 'stop'].every(name => typeof observer[name] === 'function')) fail('g09_controller_observer_invalid');
  return observer;
}

function safeError(error) {
  return error?.code || (typeof error?.message === 'string' && /^[a-z0-9_-]{1,96}$/.test(error.message) ? error.message : 'g09_controller_execution_failed');
}

/**
 * Compose the fixed G09 allocator and open-loop runner around explicitly
 * injected fixture lifecycle and observation adapters. This module owns no
 * database connection, process, listener, or resident service.
 */
export function createG09CapacityController({ profile: profileInput, resources: resourcesInput, lifecycle, observer, runner = runG09Load, perform, record = () => {} } = {}) {
  const profile = validateG09CapacityProfile(profileInput);
  const loadProfile = g09CapacityLoadProfile(profile);
  validateG09Profile(loadProfile);
  const resources = validateProjection(resourcesInput);
  const allocator = createG09CapacityAllocator({ profile, resources });
  if (typeof runner !== 'function') fail('g09_controller_runner_invalid');
  if (typeof record !== 'function') fail('g09_controller_record_invalid');

  function preflight() {
    return Object.freeze({
      status: 'PREFLIGHT_ONLY', mode: 'sealed_candidate_capacity',
      origin: profile.origin, phases: Object.freeze(profile.phases.map(row => row.name)),
      profile: Object.freeze({ maxConcurrency: profile.limits.maxConcurrency, maxOperations: profile.limits.maxOperations, drainTimeoutMs: profile.limits.drainTimeoutMs }),
      resourceProjection: Object.freeze({ customers: resources.customers.length, services: resources.services.length, invoices: resources.invoices.length, suppliers: resources.supplierAccounts.length }),
      allocation: allocator.snapshot(), executionAvailable: false,
      candidateVerified: false, capacityVerified: false, businessSuccessGenerated: false,
    });
  }

  async function run({ execute = false, runRoot, signal } = {}) {
    if (execute !== true) fail('g09_capacity_execution_disabled');
    validateRunRoot(runRoot);
    const ownedLifecycle = validateLifecycle(lifecycle);
    const ownedObserver = validateObserver(observer);
    if (typeof perform !== 'function') fail('g09_controller_performer_required');
    const abortController = new AbortController();
    const forwardAbort = () => abortController.abort();
    if (signal?.aborted) forwardAbort();
    else signal?.addEventListener('abort', forwardAbort, { once: true });
    let startAttempted = false;
    let observed = null;
    let load = null;
    let failure = null;
    let drained = false;
    let stopped = false;
    const context = Object.freeze({ profile, runRoot, signal: abortController.signal, resourceProjection: resources, allocator });
    try {
      startAttempted = true;
      await ownedLifecycle.start(context);
      await ownedObserver.start(context);
      load = await runner({ profile: loadProfile, perform: request => {
        const enriched = { ...request };
        Object.defineProperties(enriched, {
          allocator: { value: allocator, enumerable: false },
          resourceProjection: { value: resources, enumerable: false },
        });
        return perform(enriched);
      }, signal: abortController.signal, record: row => { record(row); } });
    } catch (error) {
      failure = safeError(error);
      abortController.abort();
    } finally {
      // Stop admission first, then give active work and fixture workers their
      // bounded drain window, and always close owned resources on every error.
      if (startAttempted) {
        try { await ownedLifecycle.drain({ ...context, load, failure }); drained = true; }
        catch (error) { failure ??= safeError(error); }
        try { await ownedLifecycle.stop({ ...context, load, failure }); stopped = true; }
        catch (error) { failure ??= safeError(error); }
      }
      try { observed ??= await ownedObserver.snapshot({ ...context, load, failure }); }
      catch (error) { failure ??= safeError(error); }
      try { await ownedObserver.stop({ ...context, load, failure }); }
      catch (error) { failure ??= safeError(error); }
      signal?.removeEventListener('abort', forwardAbort);
    }
    const ok = !failure && load?.ok === true && drained && stopped;
    return Object.freeze({
      status: ok ? 'OBSERVED_RUN_ONLY' : 'FAILED_CLOSED',
      mode: 'sealed_candidate_capacity', runRoot, origin: profile.origin,
      load, observer: observed, allocation: allocator.snapshot(),
      lifecycle: Object.freeze({ started: startAttempted, drained, stopped }),
      error: failure,
      candidateVerified: false, capacityVerified: false, businessSuccessGenerated: false,
    });
  }

  return Object.freeze({ preflight, run, allocator });
}

