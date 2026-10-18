#!/usr/bin/env node
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';
import { validateIntegrationURL, verifyRequiredGoTests } from './test-compiled-notifications.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export const unitSuites = {
  "internal/filestorage": [
    "TestStoragePutChecksExpectedDigestBeforeSideEffects",
    "TestLocalRequiresExistingRealAbsoluteDirectory",
    "TestLocalRoundTripPermissionsSeekAndDelete",
    "TestLocalInterruptedUploadIsTrackedByKeyAndCollectedWithoutOverwrite",
    "TestLocalTemporaryNamesRejectLinksAndDirectories",
    "TestLocalRejectsInvalidKeysForEveryOperation",
    "TestLocalRejectsLinksDirectoriesAndSpecialFiles",
    "TestLocalPutPublishesAtomicallyAndDoesNotOverwrite",
    "TestLocalOpenVerifiesSizeAndHash",
    "TestLocalOpenDoesNotBlockWhenObjectIsReplacedWithFIFO",
    "TestLocalCancellationPreservesObjectAndCancelsReader",
    "TestLocalRejectsReplacedRootAndDoesNotExposePaths",
    "TestManagerDatabaseCompatibilityAndSafeSummaries",
    "TestManagerLocationIdentityIgnoresCredentialsAndDefault",
    "TestManagerRejectsUnknownDuplicateAndMalformedConfiguration",
    "TestStorageRegistrySchemaAndOfflineConstructors",
    "TestS3SignatureMatchesPublishedAWSGetObjectVector",
    "TestS3PrivatePathStyleConditionalPutHeadRangeSeekDelete",
    "TestS3RejectsUntrustedHeadMetadata",
    "TestS3RangeProtocolFailuresAreStickyAndSanitized",
    "TestS3TransportFailuresRedirectsAndStatusesDoNotLeakOrRetry",
    "TestS3StreamErrorsDoNotLeakOrAutomaticallyRetry",
    "TestS3RejectsInvalidKeysAndMetadataBeforeNetwork",
    "TestS3ContextCancellationBeforeDuringAndAfterOpen",
    "TestS3ResponseBodiesCloseOnFailureSeekEOFAndClose",
    "TestS3ZeroLengthObjectUsesHeadAndNoInvalidRange"
  ],
  "internal/config": [
    "TestFileStorageDeploymentConfiguration",
    "TestFileStorageDeploymentRejectsInvalidWithoutLeaking"
  ],
  "internal/store": [
    "TestFileStorageInlineCompatibilityAndLimits"
  ]
};

export const integrationSuites = {
  "internal/store": [
    "TestFileStorageUploadFencePrecedesExternalWrite",
    "TestFileStorageJournalSurvivesRollbackAndCommitUnknown",
    "TestFileStorageReadRejectsLocationMissingProfileAndCorruption",
    "TestFileStorageGCLeaseAndAttachFence",
    "TestFileStorageVerifiedReadRetainsDeletionFence",
    "TestFileStorageCollectionFailuresRetainRetryableIntent",
    "TestFileStorageConcurrentJournalAndCollection",
    "TestFileStorageFailedPutRemainsTrackedAndIsNotReplayed",
    "TestFileStorageCascadeAndReplacementReleaseAllKinds",
    "TestFileStorageUpgrade166PreservesInlinePayloads"
  ],
  "internal/httpapi": [
    "TestFileStorageTicketUploadRechecksAuthorityAfterPut",
    "TestFileStorageTicketReadMergeUsesParentFirstLocks",
    "TestFileStorageDownloadWaitingReadRechecksProductEntitlement",
    "TestFileStorageLocalAPIFiveDomainsAndCompatibility",
    "TestFileStorageAPIProfileLossAndLocationDriftFailClosed",
    "TestFileStorageAPIReplacementRollbackAndCascadeRelease",
    "TestFileStorageAPIWaitingReadRechecksCredentials",
    "TestFileStorageS3APIPrivateRoundTripAndCollection",
    "TestDownloadsLifecycleBytesCategoriesAndSources",
    "TestDownloadsLoginProductAndSettingsAuthorization",
    "TestDownloadsPermissionsValidationAndLimits",
    "TestDownloadsConcurrentUpdatesAuditRollbackAndCounters",
    "TestDownloadsWaitingCredentialAndPermissionRevocation",
    "TestDownloadsSearchPaginationAndVisibleCounts",
    "TestDownloadTreeVisibilityFilteringAndMoves",
    "TestDownloadTreeCycleDeletionAndAuditRollback",
    "TestDownloadTreeConcurrentOppositeMoves",
    "TestDownloadTreeWaitingAncestorHide",
    "TestCustomerFilesLifecycleOwnershipAndByteIntegrity",
    "TestCustomerFilesAdministratorSalesScopeAndBulkAtomicity",
    "TestCustomerFilesCredentialsSettingsAndValidation",
    "TestCustomerFilesConcurrentEditsAndAuditRollback",
    "TestCustomerFilesWaitingRevocationAndSalesTransfer",
    "TestCustomerFilesSearchPaginationAndScopeCounts",
    "TestDownloadResumeAuthorizationAndReplacement",
    "TestCustomerFileResumeOwnershipAndAudit",
    "TestContentImagesDecodePermissionsAndVisibility",
    "TestTicketAttachmentsAreOwnedAndBounded",
    "TestInvoiceElectronicFileAndAdjustmentControls",
    "TestInvoiceDocumentWaitingRevocationAndReplacement",
    "TestInvoiceFileWaitingManagePermissionRevocation"
  ]
};

export async function testFileStorage(mode) {
  if (!['unit', 'integration'].includes(mode)) throw new Error('Usage: node scripts/test-file-storage.mjs --unit|--integration');
  const suites = mode === 'unit' ? unitSuites : integrationSuites;
  const env = contractBuildEnvironment();
  if (mode === 'integration') {
    env.ZJMFGO_TEST_DATABASE_URL = validateIntegrationURL(process.env.ZJMFGO_TEST_DATABASE_URL);
    env.REQUIRE_INTEGRATION = '1';
  }
  const names = [...new Set(Object.values(suites).flat())];
  const result = await runContractCommand('bash', ['scripts/go.sh', 'test', '-json', '-count=1', '-race', '-p=1', '-parallel=1', '-timeout=10m', ...Object.keys(suites).map(pkg => `./${pkg}`), '-run', `^(${names.join('|')})$`], {
    cwd: repository, env, timeoutMs: 1200000, maxBytes: 32 * 1024 * 1024, label: `file storage ${mode} tests`,
  });
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
  if (result.status !== 0 || result.signal) throw new Error(`File storage ${mode} tests failed`);
  return verifyRequiredGoTests(result.stdout, suites);
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 3 || !['--unit', '--integration'].includes(process.argv[2])) throw new Error('Usage: node scripts/test-file-storage.mjs --unit|--integration');
    process.stdout.write(`${JSON.stringify(await testFileStorage(process.argv[2].slice(2)))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
