#!/usr/bin/env node
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';
import { validateIntegrationURL, verifyRequiredGoTests } from './test-compiled-notifications.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
export const integrationSuites = {
  "internal/store": [
    "TestPurchaseGroupPricingRulesAndCents",
    "TestPurchaseDiscountAllocationConservesCents",
    "TestPurchaseFinancialReadsUseLinePricingSnapshots",
    "TestPurchaseFinancialSalesCreditCumulativeNetAndLegacy",
    "TestPurchasePricingUpgrade179PreservesRefundEvidenceAndReplay",
    "TestPurchasePricingInitialTotalsRollback",
    "TestPurchasePricingLocksSerializeMembershipAndRuleChanges",
    "TestCustomerGroupAdminLocksBeforeGroupRows",
    "TestCustomerGroupAdminPreservesLegacyReceiptsAndAuditRollback"
  ],
  "internal/httpapi": [
    "TestCustomerProductDiscountMatrixIdentityLifecycle",
    "TestCustomerGroupPricingLegacyProductGroupReceiptsReplay",
    "TestCustomerGroupPricingDefaultQuoteCheckoutAndReadModels",
    "TestCustomerGroupPricingProductRuleOverridesAndRemovalFallsBack",
    "TestCustomerGroupPricingFixedReductionPerConfiguredService",
    "TestCustomerGroupPricingScopedCouponStacksAfterGroupReduction",
    "TestCustomerGroupPricingCouponRevalidatedAfterMembershipChange",
    "TestCustomerGroupPricingPartialCartQuoteMatchesCheckout",
    "TestCustomerGroupPricingStaleQuoteAndCommittedReplay",
    "TestCustomerGroupPricingAdminPriceOverridesBypassGroupDiscount",
    "TestCustomerGroupPricingRecurringPriceFrozenAfterGroupChanges",
    "TestCustomerGroupPricingWritersTakePricingLockBeforeGroupRows",
    "TestCustomerGroupPricingSelectiveLinesAndAuditRollback",
    "TestCustomerGroupPricingRecurringCouponAndUpgradeScope",
    "TestProductIncomeCreditPurchasesUseSnapshotsAndLegacyAllocation",
    "TestProductIncomeGroupsQuantitiesFiltersPaginationAndScope",
    "TestProductIncomeQuantitiesPreserveRenewalItemsAndCreditUnits",
    "TestCreditCashReportsReceivedPeriodAndScope",
    "TestSalesReportDetailsValidation",
    "TestSalesReportDetailsAmountsScopeAndSnapshot",
    "TestSalesReportDetailsCreditUnconfiguredAndRenewal",
    "TestProductIncomeReportsDiscountedCheckoutNet",
    "TestProductIncomeAllocatesNetAcrossOrderLines",
    "TestProductIncomeAllocatesBigintNetExactly",
    "TestAdminReportsAggregatePaidCommerceData",
    "TestExtraReportsScopeAndEmptyContracts",
    "TestPromoCustomerGroupsAdminCanonicalVersionedLifecycle",
    "TestPromoCustomerGroupsValidationPermissionsAndOptions",
    "TestPromoCustomerGroupsAuditRollback",
    "TestPromoCustomerGroupsLegacyCreateAndPatchReplayFingerprints",
    "TestCheckoutAtomicAndIdempotent",
    "TestPromoCodeLifecycleAndCheckoutDiscount",
    "TestConcurrentCheckoutCannotOversell",
    "TestCheckoutRollsBackOnInvoiceFailure",
    "TestRecurringPromoSnapshotManualAutoRenewAndRefund",
    "TestRecurringPromoEligibilityAndPartialCheckout",
    "TestRecurringPromoRejectUnsupportedAndNoHistoricalGrant",
    "TestRecurringPromoRenewalAuditRollbackConcurrencyAndZeroPay",
    "TestRecurringPromoSurvivesConfigurationDelivery",
    "TestRecurringPromoSurvivesProductAndCycleDelivery",
    "TestUpgradePromoEligibilityAndSnapshots",
    "TestUpgradePromoConcurrentLimitAndCartIsolation",
    "TestAffiliatePaymentCommissionSnapshotAndConcurrentIdempotency",
    "TestAffiliatePaymentSettlementChains",
    "TestAffiliateConcurrentAttributionAndSettlementRollback"
  ]
};

export async function testCustomerGroupPricing() {
 const env = contractBuildEnvironment();
 env.ZJMFGO_TEST_DATABASE_URL = validateIntegrationURL(process.env.ZJMFGO_TEST_DATABASE_URL);
 env.REQUIRE_INTEGRATION = '1';
 const names = [...new Set(Object.values(integrationSuites).flat())];
 const result = await runContractCommand('bash', ['scripts/go.sh', 'test', '-json', '-count=1', '-race', '-p=1', '-parallel=1', '-timeout=10m', ...Object.keys(integrationSuites).map(pkg=>`./${pkg}`), '-run', `^(${names.join('|')})$`], {
  cwd: repository, env, timeoutMs: 1200000, maxBytes: 32*1024*1024, label: 'customer group purchase pricing integration tests',
 });
 process.stdout.write(result.stdout); process.stderr.write(result.stderr);
 if (result.status!==0 || result.signal) throw new Error('Customer group purchase pricing integration tests failed');
 return verifyRequiredGoTests(result.stdout,integrationSuites);
}
if (process.argv[1] && import.meta.url===pathToFileURL(path.resolve(process.argv[1])).href) {
 try {
  if (process.argv.length!==3 || process.argv[2]!=='--integration') throw new Error('Usage: node scripts/test-customer-group-pricing.mjs --integration');
  process.stdout.write(`${JSON.stringify(await testCustomerGroupPricing())}\n`);
 }catch(error){process.stderr.write(`${error.message}\n`);process.exitCode=1;}
}
