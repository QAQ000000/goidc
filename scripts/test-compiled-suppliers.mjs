#!/usr/bin/env node
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';
import { validateIntegrationURL, verifyRequiredGoTests } from './test-compiled-notifications.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export const unitSuites = {
  "internal/supplier": [
    "TestSupplierPercentConfigurationUsesAggregatedRawCostCeilings",
    "TestSupplierLifecycleCapabilitiesAndTargetProof",
    "TestSupplierConfigurationFactoryAndStableIdentity",
    "TestSupplierRegistrationRejectsUnsafeIdentityAndCapabilityMetadata",
    "TestSupplierFactoryRejectsNilDriftAndSecretBearingErrors",
    "TestSupplierLegacyAndGenericAccountIdentityStayCompatible",
    "TestSupplierAccountNormalizerOwnsCanonicalIdentity",
    "TestSupplierConsoleProtocol",
    "TestSupplierConsoleURLValidation",
    "TestPurchaseInvoiceRecoveryAndPaymentConfirmation",
    "TestAutomaticCredentialRules",
    "TestSupplierRegistrationUsesDedicatedConfiguration",
    "TestRenewalOwnershipCostPaymentAndDueConfirmation",
    "TestRevisionExcludesStockAndMapsOneTime",
    "TestCatalogProductCreateSync",
    "TestSupplierActionsSendExpectedProviderPayload",
    "TestSupplierActionRejectionIsDefinitive",
    "TestInvoiceMismatchBlocksCredit",
    "TestMismatchedPaymentHostIsUnknown",
    "TestMoneyBoundaries",
    "TestProductAvailabilityAndPaymentRules",
    "TestDCIMFixedSpecificationOptionsAreSupported",
    "TestSingleRangeDiskQuantitySelection",
    "TestAmbiguousWriteAndSecretFreeError",
    "TestUnsafeURLs",
    "TestSelectionAndJSONContract",
    "TestExistingHostWithoutInvoiceIsUnknown",
    "TestCatalogPreservesGroupsAndNumericStrings",
    "TestProductCNYPercentAndAdditionalSelect",
    "TestSupplierLinearQuantityTypesAndSetup",
    "TestSupplierGraduatedQuantityAndInvalidTiers",
    "TestSupplierFieldsAndSafePattern",
    "TestSelectionPreviewAllowsZeroStockButPurchaseRejects",
    "TestSupplierRejectsLinkageAndUnsupportedPeriods",
    "TestSupplierRetainsOnlyCompleteBillingCycles",
    "TestSupplierSimpleRevisionRemainsCompatible",
    "TestSupplierQuantityIsSubmittedAsQuantity"
  ],
  "internal/store": [
    "TestSupplierSecretsRejectOwnerDomainAndTampering",
    "TestSupplierNamedCredentialsAreOwnerAndFieldBound",
    "TestSupplierCredentialFormatNeverFallsBack",
    "TestSupplierConfigCompatibilityFreezeAndSafeSummary",
    "TestSupplierMarkupCentsRoundingAndBounds",
    "TestSupplierCatalogImportRejectsZeroOnlyQuantityRange"
  ]
};

export const integrationSuites = {
  "internal/store": ["TestSupplierConfigurationUpgrade165PreservesDeliveryIdentity", "TestSupplierLifecycleUpgrade169PreservesHistory", "TestSupplierLifecycleRequestDatabaseGuards", "TestAutoRenewalUpgrade170PreservesHistoryAndDoesNotInferSource", "TestSupplierExpiryUpgrade171PreservesHistoryAndDoesNotInferSource", "TestSupplierExpiryRequestDatabaseGuards", "TestSupplierCatalogImportUpgrade174PreservesFixedBindings", "TestSupplierCatalogImportReadTimeoutLeavesNoWrites"],
  "internal/httpapi": [
    "TestSupplierCatalogBatchImportsAllCyclesChoicesAndFields",
    "TestSupplierCatalogBatchRejectsStaleUnsupportedAndAuditFailure",
    "TestSupplierCatalogBatchPermissionValidationAndConcurrentReplay",
    "TestSupplierCatalogBatchRechecksCredentialAfterUpstreamRead",
    "TestSupplierConfigurableDeliverySelectionAndRenewal",
    "TestSupplierConfigurableSelectionFailsClosed",
    "TestSupplierExpirySuspendsWithoutCreatingDebtByCapability",
    "TestSupplierExpiryHonorsGraceAndServiceEligibility",
    "TestSupplierExpiryConcurrentScansQueueOnce",
    "TestSupplierExpiryRejectsForgedSystemActionKey",
    "TestSupplierExpiryDispatchRechecksExtendedDeadline",
    "TestSupplierExpiryFreshReadProtectsUpstreamEntitlement",
    "TestSupplierExpiryFreshReadRequeuesChangedExpiredDeadline",
    "TestSupplierExpiryEarlierUpstreamDeadlineRequiresManualReview",
    "TestSupplierExpiryManualConfirmationPreservesPauseAfterDeadlineExtension",
    "TestSupplierExpiryManualCallbackBeforeVerificationStillRecovers",
    "TestSupplierExpiryMicrosecondSourceSurvivesSecondPrecisionRenewal",
    "TestSupplierExpiryPolicyCallbackBeforeVerificationCannotGainRecoveryAuthority",
    "TestSupplierExpiryBillingPolicyCallbackBeforeVerificationCannotGainRecoveryAuthority",
    "TestSupplierExpiryPaidRenewalRefreshesSystemPause",
    "TestSupplierExpiryPaidRenewalPreservesNewManualOrPolicyHold",
    "TestSupplierLifecycleFailedResourceTerminationReleasesInventoryOnce",
    "TestSupplierLifecycleManualPausePaidBeforeConfirmationPreservesFact",
    "TestSupplierLifecycleSupportedActionsConfirmTarget",
    "TestSupplierLifecycleRejectsUnconfirmedTargetAndIdentity",
    "TestSupplierLifecycleUnknownRetainsLeaseAndVerifiesWithoutResend",
    "TestSupplierLifecycleManualActionsRequireTrustedSync",
    "TestSupplierLifecycleNotAppliedRequiresAcknowledgement",
    "TestSupplierLifecycleLegacyPendingUnsupportedBecomesManual",
    "TestSupplierLifecyclePermissionAndAuditRollback",
    "TestSupplierLifecycleVerificationRechecksPermissionAfterSync",
    "TestSupplierLifecycleConcurrentVerificationAppliesOnce",
    "TestSupplierLifecycleVerificationRechecksSessionAfterSync",
    "TestSupplierLifecycleRejectsForgedSystemActionKeys",
    "TestSupplierLifecyclePaidOverdueSuspensionRecoversByCapability",
    "TestSupplierLifecyclePaidRecoveryPreservesOtherSuspensionReasons",
    "TestSupplierLifecyclePaidRecoveryDispatchRechecksNewDebt",
    "TestSupplierLifecyclePaidRecoveryConcurrentScansQueueOnce",
    "TestOverdueSuspendSkipsUnsupportedModesWithAudit",
    "TestOverdueSuspendStillQueuesSupportedModes",
    "TestOverdueSuspendSupplierMatchesManualActionConstraint",
    "TestOverdueSuspendProgressPastUnsupportedBatch",
    "TestOverdueSuspendProgressReconsidersChangedModes",
    "TestOverdueSuspendProgressEvidenceMatchesCurrentMode",
    "TestOverdueSuspendProgressConcurrentInvoices",
    "TestOverdueSuspendProgressRollsBackOnQueueFailure",
    "TestSupplierAPISettingsSchemaPermissionsAndConnectionProbe",
    "TestResourceAPISettingsVersionIdempotencyAndBearerGate",
    "TestResourceAPIKeyEligibilityGates",
    "TestSupplierMaintainedLifecycleRules",
    "TestSupplierInflightActiveSyncCannotReviveDeleted",
    "TestSupplierHostSyncCallback",
    "TestSupplierCatalogEntryReadOnlyAndManageBoundary",
    "TestSupplierConsolePasswordRescueAndReinstall",
    "TestSupplierReadonlySyncFailureReleasesAccount",
    "TestSupplierCustomerConsole",
    "TestSupplierCustomerConsoleFencesChanges",
    "TestSupplierCapabilitiesWithoutCredentials",
    "TestSupplierCapabilitiesInvalidSnapshot",
    "TestSupplierCapabilitiesUseServiceSnapshot",
    "TestMissingSupplierDisablePreservesIdentityCredentialsAndCAS",
    "TestMissingSupplierDisableAuditFailureAndPermission",
    "TestMissingSupplierDisableKeepsRunningJobGuard",
    "TestMissingSupplierDisableRechecksRevocationAfterLock",
    "TestSupplierJobsScopeSecretsAndSafeRetry",
    "TestSupplierJobRetrySameKeyAcrossServices",
    "TestSupplierOrdersSnapshotScopeAndPagination",
    "TestSupplierReadOnlySyncCrashReleasesAccount",
    "TestSupplierDispatchedWriteCrashStillRequiresVerification",
    "TestSupplierMixedCrashRecoverySeparatesReadOnlyAndWriteJobs",
    "TestSupplierCrashedReadOnlyJobExhaustedAttemptsFailsClosed",
    "TestSupplierRenewalUsesOneConnection",
    "TestSupplierRenewalFullPoolMakesProgress",
    "TestSupplierRenewalQuotationRechecksConcurrentChanges",
    "TestSupplierRenewalPaysUpstreamBeforeExtendingDue",
    "TestSupplierRenewalRefundBeforeDispatchPreservesDue",
    "TestSupplierAutoRenewalQueuesOnlyOneUpstreamRenewal",
    "TestSupplierAutoRenewalRefreshesStaleDeadlineWithoutCharging",
    "TestSupplierAutoRenewalInsufficientBalancePreservesOriginalInvoice",
    "TestSupplierAutoRenewalNotificationWaitsForConfirmedPeriod",
    "TestSupplierRenewalRefreshesDueSuspensionAfterPayment",
    "TestSupplierRenewalDeferredDueRecoveryRequiresTrustedSource",
    "TestSupplierRenewalPreservesManualSuspension",
    "TestSupplierRenewalFailureCannotResumeDueSuspension",
    "TestSupplierAutoRenewalPaymentRechecksOriginalIntent",
    "TestAutoRenewalPaymentRejectsCancellationApprovedAfterCreation",
    "TestSupplierAutoRenewalRejectsManualInvoiceKeyCollision",
    "TestSupplierAutoRenewalSourceDatabaseGuards",
    "TestSupplierAutoRenewalPaymentRechecksSupplierAccount",
    "TestSupplierAutoRenewalDispatchFailuresRemainRecoverable",
    "TestSupplierRenewalWorkerSIGKILLRecovery",
    "TestSupplierUnattendedInvoiceRecoveryPaymentAndActivation",
    "TestSupplierWorkerSIGKILLRecovery",
    "TestSupplierManagementImportCheckoutAndDelivery",
    "TestSupplierPeriodicSyncCanReuseCompletedJob",
    "TestSupplierProductMismatchAndDuplicateResourceRequireVerification",
    "TestSupplierUnknownOutcomeAccountBlockAndReconciliation",
    "TestSupplierAccountSerializesConcurrentPurchases",
    "TestSupplierCredentialsAreOwnerBound",
    "TestSupplierPeriodicSyncAfterRemoteDeletionReleasesOnce",
    "TestCompiledSupplierAccountConfigurationAndIsolation",
    "TestCompiledSupplierSchemaValidationAndSafeStop",
    "TestCompiledSupplierMissingImplementationOnlyStops",
    "TestCompiledSupplierAccountAuditRollback",
    "TestCompiledSupplierRuntimeBindingSyncAndRenewal",
    "TestCompiledSupplierUnknownWriteNeverAutomaticallyResends",
    "TestCompiledSupplierLegacyAccountUpgradeKeepsBinding",
    "TestCompiledSupplierAccountJSONHidesNamedCredentials",
    "TestCompiledSupplierLockWaitRejectsExpiredSession"
  ]
};

export async function testCompiledSuppliers(mode) {
  if (!['unit', 'integration'].includes(mode)) throw new Error('Usage: node scripts/test-compiled-suppliers.mjs --unit|--integration');
  const suites = mode === 'unit' ? unitSuites : integrationSuites;
  const env = contractBuildEnvironment();
  if (mode === 'integration') {
    env.ZJMFGO_TEST_DATABASE_URL = validateIntegrationURL(process.env.ZJMFGO_TEST_DATABASE_URL);
    env.REQUIRE_INTEGRATION = '1';
    env.ZJMFGO_WORKER_CRASH_ACCEPTANCE = '1';
  }
  const names = [...new Set(Object.values(suites).flat())];
  const timeout = mode === 'integration' ? '-timeout=15m' : '-timeout=10m';
  const result = await runContractCommand('bash', ['scripts/go.sh', 'test', '-json', '-count=1', '-race', '-p=1', '-parallel=1', timeout, ...Object.keys(suites).map(pkg => `./${pkg}`), '-run', `^(${names.join('|')})$`], {
    cwd: repository, env, timeoutMs: 1200000, maxBytes: 32 * 1024 * 1024, label: `compiled suppliers ${mode} tests`,
  });
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
  if (result.status !== 0 || result.signal) throw new Error(`Compiled supplier ${mode} tests failed`);
  return verifyRequiredGoTests(result.stdout, suites);
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 3 || !['--unit', '--integration'].includes(process.argv[2])) throw new Error('Usage: node scripts/test-compiled-suppliers.mjs --unit|--integration');
    process.stdout.write(`${JSON.stringify(await testCompiledSuppliers(process.argv[2].slice(2)))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
