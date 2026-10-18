#!/usr/bin/env node
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';
import { validateIntegrationURL, verifyRequiredGoTests } from './test-compiled-notifications.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export const unitSuites = {
  'internal/payment': [
    'TestEpayGatewayBuildsLegacySignedPaymentLink',
    'TestEpayRejectsAmbiguousAndUnsafeInput',
    'TestEpayGatewayDecodesSignedCallback',
    'TestEpayQueryVerifiedResponseAndWire',
    'TestEpayQueryRejectsAmbiguousAndMismatchedResponses',
    'TestEpayQueryNoRedirectTLSVerificationAndRedactedTimeout',
    'TestEpayQueryDoesNotUseDownloadProxy',
    'TestPaymentRegistryOwnsFixedMethodsAndIndependentConfiguration',
    'TestPaymentRegistryRejectsAmbiguousRegistrationAndWrongFactories',
    'TestBuiltinPaymentMethodsStayCompatibleAndCreateIntentsLocally',
    'TestMockCallbackProtocolStrictLocatorAndJSONAcknowledgement',
    'TestEpayCallbackProtocolStrictFormLocatorAndLegacyAcknowledgement',
    'TestEpayFactoryGenericConfigurationAndLegacyNamespaceRemainCompatible',
    'TestPaymentMerchantScopeMatchesHistoricalCanonicalIdentity',
  ],
  'internal/store': ['TestPaymentRuntimeSnapshotEncryptionAndIdentity', 'TestPaymentRuntimeSnapshotRejectsUnsupportedEnvelope'],
};

export const integrationSuites = {
  'internal/store': ['TestPaymentRuntimeUpgrade164PreservesHistoricalIdentity', 'TestPaymentRuntimeMalformedHistoryNeverUsesCurrentCredentials'],
  'internal/httpapi': [
    'TestCompiledPaymentCatalogAndInstanceIsolation',
    'TestCompiledPaymentHistoricalCallbackUsesFrozenCredentials',
    'TestCompiledPaymentHistoricalQueryAndExpiry',
    'TestCompiledPaymentSchemaAndConfigurationErrors',
    'TestCompiledPaymentCallbackValidationAndReceiptIdentity',
    'TestCompiledPaymentRefundMerchantScopes',
    'TestCompiledPaymentTrustedCloseAndLateReceipt',
    'TestCompiledPaymentUnknownCloseAndIndependentHistoricalLookup',
    'TestCompiledPaymentAuditFailureRollsBackFacts',
    'TestPaymentIntentBindsSelectedChannelInstance',
    'TestPaymentSchemaMismatchRejectsNewIntentAndPreservesSnapshot',
    'TestDefaultPaymentContractRejectsSchemaWithoutRequiringFallbackFields',
    'TestSelectedPaymentInstanceCallbackSurvivesDisable',
    'TestSelectedPaymentInstanceAutomaticQueryUsesSnapshot',
    'TestEpayPaymentLifecycleAndReplay',
    'TestEpayCallbackRollbackAndInvalidFields',
    'TestEpayPendingGuardsAndServiceSettlement',
    'TestPaymentMockDisabledByDefaultAndExplicitConfiguration',
    'TestPaymentSettingsSecretsPermissionsVersionsAndAuditRollback',
    'TestPaymentSettingsDynamicAPIWorkerAndDisabledCallbacks',
    'TestPaymentSettingsMockOnlyAfterClearingMerchantKey',
    'TestPaymentSettingsValidationFallbackAndStaleIntent',
    'TestPaymentSettingsSessionRecheckedAfterWaiting',
    'TestPaymentExpiryMerchantRotationAndLateSettlement',
    'TestPaymentExpiryLegacyBackfillAndLeaseGuard',
    'TestLegacyTerminalPaymentLostCredentialsAllowNewMerchant',
    'TestLegacyNonterminalPaymentLostCredentialsBlockNewMerchant',
    'TestPaymentReconciliationPendingRollbackPaidAndLateCallback',
    'TestPaymentReconciliationConcurrentChecksAndCallbackWins',
    'TestPaymentReconciliationRechecksPermissionAndCustomerTransfer',
    'TestPaymentReconciliationInvalidResultAndExpiredLease',
    'TestAutomaticPaymentMonitoringSuccessAndLateCallback',
    'TestAutomaticPaymentMonitoringLeaseAndCallbackWins',
    'TestAutomaticPaymentMonitoringExpiredLeaseFencingAndRecovery',
    'TestAutomaticPaymentMonitoringBackoffExhaustionAndManualRestart',
    'TestAutomaticPaymentMonitoringFailuresAndRollback',
    'TestAutomaticPaymentMonitoringConfiguredProvidersOnly',
    'TestPaymentCloseHTTPClosedReplayAndNewIntent',
    'TestPaymentCloseEpayUnavailableEvenExpired',
    'TestPaymentCloseUnknownFreezesAndPreservesOriginalRequest',
    'TestPaymentCloseHTTPReadWriteScopeAndBearerBoundaries',
    'TestPaymentCloseRechecksRevocationScopeAndSession',
    'TestPaymentCloseConcurrentReservationAndCallbackPriority',
    'TestPaymentCloseLateSuccessCreatesFrozenMoneyFact',
    'TestPaymentCloseAuditRollbackAndInvalidLease',
    'TestPaymentCloseTopupReservationReuseCancelAndLateAnomaly',
    'TestPaymentCloseActiveQueryAndPaidProviderResultsDoNotUnlock',
    'TestPaymentCloseVerificationUnknownAndUnsupportedStayFrozen',
    'TestPaymentCloseVerificationSignedEpayCallbackAndReviewCompete',
    'TestExternalRefundHTTPPermissionsLifecycleAndReplay',
    'TestExternalRefundHTTPConcurrentCreateAndConfirm',
    'TestExternalRefundHTTPRejectScopeAndFailureAtomicity',
    'TestEpayExternalRefundRefusalPreservesSettlement',
    'TestPaymentAnomalyRefundLifecycleGroupsAndSeparateMoney',
    'TestPaymentAnomalySharedPayoutClaimAcrossRefundWorkflows',
    'TestPaymentResolutionEffectiveGuardsAcrossWorkflows',
    'TestMaintenancePaymentWebhookAndMFALogin',
  ],
};

export async function testCompiledPayments(mode) {
  if (!['unit', 'integration'].includes(mode)) throw new Error('Usage: node scripts/test-compiled-payments.mjs --unit|--integration');
  const suites = mode === 'unit' ? unitSuites : integrationSuites;
  const env = contractBuildEnvironment();
  if (mode === 'integration') {
    env.ZJMFGO_TEST_DATABASE_URL = validateIntegrationURL(process.env.ZJMFGO_TEST_DATABASE_URL);
    env.REQUIRE_INTEGRATION = '1';
  }
  const names = [...new Set(Object.values(suites).flat())];
  const result = await runContractCommand('bash', ['scripts/go.sh', 'test', '-json', '-count=1', '-race', '-p=1', '-parallel=1', '-timeout=10m', ...Object.keys(suites).map(pkg => `./${pkg}`), '-run', `^(${names.join('|')})$`], {
    cwd: repository, env, timeoutMs: 1200000, maxBytes: 32 * 1024 * 1024, label: `compiled payments ${mode} tests`,
  });
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
  if (result.status !== 0 || result.signal) throw new Error(`Compiled payment ${mode} tests failed`);
  return verifyRequiredGoTests(result.stdout, suites);
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 3 || !['--unit', '--integration'].includes(process.argv[2])) throw new Error('Usage: node scripts/test-compiled-payments.mjs --unit|--integration');
    process.stdout.write(`${JSON.stringify(await testCompiledPayments(process.argv[2].slice(2)))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
