#!/usr/bin/env node
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';
import { validateIntegrationURL, verifyRequiredGoTests } from './test-compiled-notifications.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export const unitSuites = {
  'internal/certification': [
    'TestFactoryBuildsIndependentConfiguredProviders',
    'TestFactoryErrorsAndMissingImplementationsRemainErrors',
    'TestLegacyRegistrationAndManualContractRemainCompatible',
  ],
  'cmd/worker': ['TestCertificationWorkerCancellationAndErrorPrivacy'],
};

export const integrationSuites = {
  'internal/store': [
    'TestCertificationProviderInstancesAndFrozenCredentials',
    'TestCertificationProviderUnknownSubmitQueriesOnly',
    'TestCertificationProviderLeaseAndManualFencing',
    'TestCertificationProviderDispatchGatesAndQueryLimits',
    'TestCertificationProviderResultSubjectAndRollback',
    'TestCertificationProviderUpgrade163PreservesManualApplications',
    'TestCertificationProviderLeaseRecheckedAfterJobLockWait',
    'TestCertificationProviderSettingsClaimLockOrder',
    'TestCertificationProviderNameContentionRollsBack',
  ],
  'internal/httpapi': [
    'TestCertificationSubmissionReceiptConcurrencyAndRollback',
    'TestCertificationSubmissionReplayRechecksCredentialAfterWait',
    'TestCertificationProviderApprovalEnablesOrderAndAPIKey',
    'TestCertificationSettingsContractAndPermissions',
    'TestCertificationSettingsExplicitInstanceAndReplay',
    'TestCertificationSettingsRejectsUnavailableInstanceWithoutMutation',
    'TestCertificationSettingsLocksSettingsBeforeInstance',
    'TestCertificationCompanyConversionIsTransactionalAndIdempotent',
    'TestCertificationConversionReceiptWaitRechecksSession',
    'TestCertificationConversionReceiptWaitRechecksMasterSwitch',
    'TestCertificationScopeAndCredentialRecheckedAfterProfileWait',
    'TestCertificationReviewHoldsCustomerScopeUntilCommit',
    'TestCertificationReviewRechecksPermissionAfterRoleLockWait',
    'TestCertificationDecisionNameAndProfileVersionRollbackTogether',
    'TestCertificationReplayRequiresCurrentScopeAndPermission',
    'TestCertificationNameUpdatesProfileVersionAndPreservesFields',
    'TestCertificationReviewHonorsCustomerScope',
    'TestCertificationMasterSwitchGatesSubmissionAndConversion',
    'TestCertificationAccountViewExposesEnabledSwitch',
    'TestCertificationAutoUpdateNameOnApproval',
    'TestCertificationAutoUpdateNameAllowsDuplicateNames',
    'TestCertificationPhoneMatchFollowsLegacyLenientSemantics',
    'TestResourceAPIKeyEligibilityGates',
    'TestOrderSettingsAdmissionAndReceipts',
  ],
};

export async function testCompiledCertification(mode) {
  if (!['unit', 'integration'].includes(mode)) throw new Error('Usage: node scripts/test-compiled-certification.mjs --unit|--integration');
  const suites = mode === 'unit' ? unitSuites : integrationSuites;
  const env = contractBuildEnvironment();
  if (mode === 'integration') {
    env.ZJMFGO_TEST_DATABASE_URL = validateIntegrationURL(process.env.ZJMFGO_TEST_DATABASE_URL);
    env.REQUIRE_INTEGRATION = '1';
  }
  const names = [...new Set(Object.values(suites).flat())];
  const result = await runContractCommand('bash', ['scripts/go.sh', 'test', '-json', '-count=1', '-race', '-p=1', '-parallel=1', '-timeout=10m', ...Object.keys(suites).map(pkg => `./${pkg}`), '-run', `^(${names.join('|')})$`], {
    cwd: repository, env, timeoutMs: 1200000, maxBytes: 32 * 1024 * 1024, label: `compiled certification ${mode} tests`,
  });
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
  if (result.status !== 0 || result.signal) throw new Error(`Compiled certification ${mode} tests failed`);
  return verifyRequiredGoTests(result.stdout, suites);
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 3 || !['--unit', '--integration'].includes(process.argv[2])) throw new Error('Usage: node scripts/test-compiled-certification.mjs --unit|--integration');
    process.stdout.write(`${JSON.stringify(await testCompiledCertification(process.argv[2].slice(2)))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
