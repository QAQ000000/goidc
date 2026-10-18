#!/usr/bin/env node
import { constants } from 'node:fs';
import { access, lstat, realpath } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const modulePrefix = 'github.com/codex/zjmfgo/';

// Exact names make removal, renaming and accidental -run mismatches fail.
export const unitSuites = {
  'internal/channel': ['TestRegistryRejectsDuplicateAndListsSorted', 'TestNormalizeConfigEnforcesContractWithoutEchoingValues', 'TestDescriptorRejectsInvalidSchemasAndDefensivelyCopiesDefaults', 'TestSecretSyntaxValidatorsRequireFactoriesAndProtectInputMap'],
  'internal/module': ['TestRegistryRegistersDescriptorAndBuildsTypedImplementation', 'TestRegistryRejectsDuplicateDescriptor'],
  'internal/modules/manifest': ['TestDefaultProfileMatchesCompiledModules', 'TestManifestCLIIsConfigurationIndependentAndStrict'],
  'internal/notification': [
    'TestSMTPRequiresSTARTTLSAndRedactsErrors', 'TestSMTPPlaintextMessageAndUTF8Headers',
    'TestMailDispatchUsesRegisteredFactoryAndIndependentSecrets', 'TestSMSDispatchUsesRegisteredFactoryWithoutEndpointAssumptions', 'TestSMTPFactoryValidatesBeforeConnectingAndUsesNamedPassword',
    'TestSMSGatewayEvidencePreservesOriginalBody', 'TestSMSWithEvidenceFailuresRemainPrivate', 'TestSMSWithEvidenceGenericAcceptanceSurvivesLargeBody', 'TestSMSGatewayEvidenceRetainsPartialRead', 'TestSMSIgnoresDefaultTransportProxy', 'TestSMSProtocolAndFailurePrivacy', 'TestIdcsmartSMSProtocol', 'TestIdcsmartSMSLegacyResponseVariants', 'TestIdcsmartSMSFailureDoesNotExposeResponse', 'TestIdcsmartSign', 'TestIdcsmartStatusOK',
    'TestTelegramCompiledRegistryAndLocalValidation', 'TestTelegramSendPlainTextAndStrictAcceptance', 'TestTelegramDeadlineLengthRedirectAndTransportSafety', 'TestTelegramWebhookExplicitConfigurationContract',
    'TestNotificationCenterDispatchPreservesProviderKnowledge', 'TestNotificationCenterRejectsUnavailableOrChangedEligibilityBeforeSending', 'TestNotificationCenterPersistenceAndLeaseFailuresNeverResendOrExposeErrors',
  ],
};

export const integrationSuites = {
  'internal/store': [
    'TestNotificationCenterDefaultsAndNoHistoricalBackfill', 'TestNotificationCenterBindingIsPrivateSingleUseAndNotConsent', 'TestNotificationCenterChallengesRevocationAndCurrentAuthority', 'TestNotificationCenterConsentCASAndNoTargetOwnershipTransfer', 'TestNotificationCenterLegacyLinksNeverEnterNewWorker', 'TestNotificationCenterInboxAtomicDeliveryFrozenTemplateAndOwnership', 'TestNotificationCenterClaimRechecksCurrentConsentAndOwnership', 'TestNotificationCenterActiveRecheckLeaseRecoveryAndUnknownNeverRetry', 'TestNotificationCenterRetriesOnlyExplicitRateLimitAndStopsAtFive', 'TestNotificationCenterConcurrentClaimsExactlyOnce', 'TestNotificationCenterOversizedRenderedTemplateCannotPoisonQueue', 'TestNotificationCenterCompletionWaitingPastLeaseIsFenced', 'TestNotificationCenterRollbackAndAdminMetadataScope',
    'TestNotificationCenterUpgradeFrom160',
  ],
  'internal/httpapi': [
    'TestChannelConfigurationNamedCredentialsAndRollback', 'TestChannelConfigurationRejectsPollutionAndMissingContracts', 'TestChannelConfigurationMissingAndIncompatibleInstancesRemainManageable', 'TestChannelInstancesCatalogueAndSecretLifecycle', 'TestPaymentIntentBindsSelectedChannelInstance',
    'TestCompiledMailAdapterRunsThroughInstanceOutbox', 'TestCompiledSMSAdapterRunsThroughInstanceOutbox', 'TestNotificationEnableValidatesLocalTransportConfiguration', 'TestDefaultIDCSmartSMSUsesInstanceConfiguration', 'TestLegacySMTPResolversCannotBypassInstanceSchema', 'TestCompiledInstancesCanStartWithoutCredentials',
    'TestPaymentSchemaMismatchRejectsNewIntentAndPreservesSnapshot', 'TestDefaultPaymentContractRejectsSchemaWithoutRequiringFallbackFields',
    'TestMissingSupplierDisablePreservesIdentityCredentialsAndCAS', 'TestMissingSupplierDisableAuditFailureAndPermission', 'TestMissingSupplierDisableKeepsRunningJobGuard', 'TestMissingSupplierDisableRechecksRevocationAfterLock',
    'TestSMTPSettingsSecurityAndDynamicResolution', 'TestSMTPInstanceOptionalFieldsReachRealTransport', 'TestSMSSettingsAndEncryptedOutbox',
    'TestNotificationCenterBindingPrivacyAndConsent', 'TestNotificationCenterBindingRequiresLiveOriginatingSession', 'TestNotificationCenterWebhookRotationDuringBindingRollsBack', 'TestNotificationCenterActualAdapterWorkerAndInbox', 'TestNotificationCenterAdminScopeAndAuditRollback', 'TestNotificationCenterInvalidSecretRejectedAtSave',
  ],
  'internal/notification': ['TestTelegramProtocolResultsPersistThroughNotificationStore', 'TestNotificationCenterProcessRecovery'],
};

export function validateIntegrationURL(raw) {
  let url;
  try { url = new URL(raw); } catch { throw new Error('Integration gate requires an explicit loopback PostgreSQL _test URL'); }
  const query = [...url.searchParams.entries()];
  if (!['postgres:', 'postgresql:'].includes(url.protocol) || url.hostname !== '127.0.0.1' || !url.port || !/^\/[A-Za-z0-9_]+_test$/.test(url.pathname) || url.hash || query.length > 1 || query.some(([key, value]) => key !== 'sslmode' || !['disable', 'prefer'].includes(value))) {
    throw new Error('Integration gate requires an explicit loopback PostgreSQL _test URL without connection overrides');
  }
  return raw;
}

export async function integrationBinaryEnvironment(source = process.env) {
  const keys = ['ZJMFGO_UPGRADE_API_BINARY', 'ZJMFGO_UPGRADE_WORKER_BINARY', 'ZJMFGO_UPGRADE_BINARY_ACCEPTANCE'];
  if (keys.every(key => source[key] === undefined)) return {};
  if (source.ZJMFGO_UPGRADE_BINARY_ACCEPTANCE !== '1' || keys.some(key => typeof source[key] !== 'string' || source[key] === '')) throw new Error('Upgrade binary acceptance requires both executable paths and the explicit flag 1');
  const out = {};
  for (const key of keys.slice(0, 2)) {
    const filename = source[key];
    if (!path.isAbsolute(filename) || path.normalize(filename) !== filename) throw new Error('Upgrade binary paths must be absolute regular executable files');
    try {
      const stat = await lstat(filename);
      if (!stat.isFile() || stat.size === 0 || !(stat.mode & 0o111) || await realpath(filename) !== filename) throw new Error('invalid');
      await access(filename, constants.X_OK);
    } catch { throw new Error('Upgrade binary paths must be absolute regular executable files'); }
    out[key] = filename;
  }
  out.ZJMFGO_UPGRADE_BINARY_ACCEPTANCE = '1';
  return out;
}

export function verifyRequiredGoTests(output, suites) {
  const required = new Set(Object.entries(suites).flatMap(([pkg, tests]) => tests.map(test => `${modulePrefix}${pkg}:${test}`)));
  const passed = new Set(), started = new Set(), packages = new Set();
  for (const line of output.split('\n').filter(line => line.trim())) {
    let event;
    try { event = JSON.parse(line); } catch { throw new Error('Go test gate received invalid JSON output'); }
    if (!event || typeof event !== 'object' || typeof event.Action !== 'string' || typeof event.Package !== 'string') throw new Error('Go test gate received an invalid event');
    if (event.Action === 'skip' || event.Action === 'fail') throw new Error(`Required Go test did not pass: ${event.Package}:${event.Test ?? '(package)'}`);
    if (event.Test) {
      const key = `${event.Package}:${event.Test}`;
      if (event.Action === 'run') started.add(key);
      if (event.Action === 'pass' && started.has(key)) passed.add(key);
    } else if (event.Action === 'pass') packages.add(event.Package);
  }
  const missing = [...required].filter(key => !passed.has(key));
  if (missing.length) throw new Error(`Required Go tests did not execute: ${missing.join(', ')}`);
  for (const pkg of Object.keys(suites)) if (!packages.has(`${modulePrefix}${pkg}`)) throw new Error(`Go test package did not pass: ${pkg}`);
  return { tests: required.size, packages: packages.size, skipped: 0 };
}

export async function testCompiledNotifications(mode) {
  if (!['unit', 'integration'].includes(mode)) throw new Error('Usage: node scripts/test-compiled-notifications.mjs --unit|--integration');
  const suites = mode === 'unit' ? unitSuites : integrationSuites;
  const env = contractBuildEnvironment();
  if (mode === 'integration') {
    env.ZJMFGO_TEST_DATABASE_URL = validateIntegrationURL(process.env.ZJMFGO_TEST_DATABASE_URL);
    Object.assign(env, await integrationBinaryEnvironment());
    env.REQUIRE_INTEGRATION = '1';
    env.ZJMFGO_WORKER_CRASH_ACCEPTANCE = '1';
  }
  const names = [...new Set(Object.values(suites).flat())];
  const result = await runContractCommand('bash', ['scripts/go.sh', 'test', '-json', '-count=1', '-race', '-p=1', '-parallel=1', '-timeout=12m', ...Object.keys(suites).map(pkg => `./${pkg}`), '-run', `^(${names.join('|')})$`], {
    cwd: repository, env, timeoutMs: mode === 'unit' ? 600000 : 1800000, maxBytes: 32 * 1024 * 1024, label: `compiled notification ${mode} tests`,
  });
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
  if (result.status !== 0 || result.signal) throw new Error(`Compiled notification ${mode} tests failed`);
  return verifyRequiredGoTests(result.stdout, suites);
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 3) throw new Error('Usage: node scripts/test-compiled-notifications.mjs --unit|--integration');
    const mode = process.argv[2].replace(/^--/, '');
    if (`--${mode}` !== process.argv[2]) throw new Error('Usage: node scripts/test-compiled-notifications.mjs --unit|--integration');
    process.stdout.write(`${JSON.stringify(await testCompiledNotifications(mode))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
