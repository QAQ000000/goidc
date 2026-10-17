# 历史审计与未接通工具封存

封存基线：`65af830`，2026-10-17。此包保存精简前全部 docs 原始字节，以及下列未接通的 G09 准备代码；不是新的验收结论。活动文档和测试以 [项目入口](../reconstruction.md)、[测试说明](../testing.md)及[容量验证](../capacity.md)为准。

原始路径、逐文件 SHA-256 见 [MEMBERS.sha256](MEMBERS.sha256)。压缩包校验：

```bash
# 在仓库根目录执行
sha256sum -c docs/archive/SHA256SUMS
# 查看某份原文，不写工作目录
tar -xOzf docs/archive/maintenance-20261017.tar.gz docs/reconstruction.md
# 在独立目录恢复全部原始文件，再核验逐文件摘要
restore_dir="$(mktemp -d /tmp/goidc-audit-restore.XXXXXXXX)"
tar -xzf docs/archive/maintenance-20261017.tar.gz -C "$restore_dir"
(cd "$restore_dir" && sha256sum -c /www/wwwroot/goidc/docs/archive/MEMBERS.sha256)
```

历史文档的相对链接、截图和外部日志引用保持原样；解包不保证仓库外证据仍存在，也不把历史 PASS 当成当前通过。需要恢复某项开发时，在独立目录提取对应成员并重新核对当前接口和迁移。不要直接覆盖当前源码。

以下索引承接原活动文档中的历史链接。原来的章节片段保留在包内原文。

<a id="f-e5fc9090aaa0"></a>
- `docs/acceptance/g09-runtime-process-20261017.md`

<a id="f-d93dda6ca480"></a>
- `docs/admin-auth-security-batch.md`

<a id="f-60e03a75798a"></a>
- `docs/admin-batch-access-sales-acceptance-20261011.md`

<a id="f-bf9b577744e3"></a>
- `docs/admin-batch-admin-orders-acceptance-20261011.md`

<a id="f-43a541711de8"></a>
- `docs/admin-batch-admin-tickets-acceptance-20261011.md`

<a id="f-ed88b40abed4"></a>
- `docs/admin-batch-affiliate-sales-acceptance-20261013.md`

<a id="f-8c446435009a"></a>
- `docs/admin-batch-content-site-acceptance-20261011.md`

<a id="f-49ed3b1557b0"></a>
- `docs/admin-batch-customer-extension-acceptance.md`

<a id="f-b1bfb8d5cbf8"></a>
- `docs/admin-batch-customer-orders-acceptance-20261011.md`

<a id="f-b520556d3f6f"></a>
- `docs/admin-batch-customer-ticket-acceptance.md`

<a id="f-590d5932478c"></a>
- `docs/admin-batch-reports-operations-acceptance-20261011.md`

<a id="f-b38b6c374f8a"></a>
- `docs/admin-batch-security-acceptance-20261011.md`

<a id="f-03b05fcef0bd"></a>
- `docs/admin-batch-settings-marketing-acceptance-20261011.md`

<a id="f-7c0786c71b29"></a>
- `docs/admin-batch-ticket-logs-acceptance-20261011.md`

<a id="f-774baca14c7d"></a>
- `docs/admin-batch-traffic-acceptance-20261011.md`

<a id="f-b301f84e8e20"></a>
- `docs/admin-compiled-apps-acceptance-20261013.md`

<a id="f-e475cf8da941"></a>
- `docs/admin-configuration-acceptance.md`

<a id="f-b02822aafde3"></a>
- `docs/admin-credit-lifecycle-batch.md`

<a id="f-7be8e6129f77"></a>
- `docs/admin-credit-management-batch.md`

<a id="f-de61a3567672"></a>
- `docs/admin-credit-supplier-acceptance-20261012.md`

<a id="f-3ccf3539a9a2"></a>
- `docs/admin-credit-supplier-plan-20261012.md`

<a id="f-c0b07f02ff9a"></a>
- `docs/admin-entry-pages-acceptance-20261013.md`

<a id="f-e66397c7ca7d"></a>
- `docs/admin-mofang-batch2-acceptance.md`

<a id="f-775cee44ecb6"></a>
- `docs/admin-mofang-batch3-acceptance.md`

<a id="f-8734f173c3b0"></a>
- `docs/admin-mofang-batch4-settings-acceptance.md`

<a id="f-0406fcb8d816"></a>
- `docs/admin-mofang-batch5-customer-resources-acceptance.md`

<a id="f-eb02c5090779"></a>
- `docs/admin-options-order-acceptance-20261012.md`

<a id="f-cc55b44761e8"></a>
- `docs/admin-parallel-acceptance-20261012.md`

<a id="f-9faf925000ae"></a>
- `docs/admin-restore-interaction-acceptance.md`

<a id="f-f5525b232412"></a>
- `docs/admin-shell-acceptance.md`

<a id="f-3fc6167da9d6"></a>
- `docs/admin-supplier-api-settings-acceptance-20261012.md`

<a id="f-54d00445dd14"></a>
- `docs/admin-supplier-orders-batch.md`

<a id="f-f648e973e8d1"></a>
- `docs/admin-supplier-queues-batch.md`

<a id="f-56d4dfcb86eb"></a>
- `docs/affiliate-settlement-acceptance.md`

<a id="f-8f6a35cb5890"></a>
- `docs/api-log-navigation-acceptance-20261016.md`

<a id="f-48c7368d605f"></a>
- `docs/batch4-acceptance.md`

<a id="f-7c2fcfc718cc"></a>
- `docs/close-expired-recovery-browser-acceptance-20261017.md`

<a id="f-bcd78145bd68"></a>
- `docs/code-freeze-20261015.md`

<a id="f-c01c826de1a1"></a>
- `docs/code-review-verification-20261013.md`

<a id="f-e13a9e66941a"></a>
- `docs/contract-settings-browser-evidence-20261016.json`

<a id="f-0813795615b8"></a>
- `docs/core-replacement-goal-20261016.md`

<a id="f-b9e9f082a11e"></a>
- `docs/core-replacement-stage1-acceptance-20261016.md`

<a id="f-bed9d288a9ac"></a>
- `docs/core-replacement-stage1-browser-evidence-20261016.json`

<a id="f-d383850d251d"></a>
- `docs/customer-credit-portal-acceptance-20261016.md`

<a id="f-cae350ca30b2"></a>
- `docs/customer-credit-portal-browser-evidence-20261016.json`

<a id="f-afad91eaba83"></a>
- `docs/customer-credit-portal-test-20261016.md`

<a id="f-637a6f3af9ff"></a>
- `docs/customer-field-admin-browser-evidence-20261016.json`

<a id="f-b448f742916b"></a>
- `docs/customer-field-admin-http-test-20261016.md`

<a id="f-fc6ea858338d"></a>
- `docs/customer-field-admin-restoration-acceptance-20261016.md`

<a id="f-7a77611dda90"></a>
- `docs/customer-online-topup-acceptance-20261016.md`

<a id="f-6a402c8cdaf8"></a>
- `docs/customer-online-topup-backend-acceptance-20261016.md`

<a id="f-f00be718a565"></a>
- `docs/customer-registration-fields-acceptance-20261016.md`

<a id="f-617153f5d909"></a>
- `docs/customer-registration-fields-browser-evidence-20261016.json`

<a id="f-af7f8238dda8"></a>
- `docs/customer-registration-fields-test-20261016.md`

<a id="f-f28676857f78"></a>
- `docs/customer-topup-browser-evidence-20261016.json`

<a id="f-59a71e524213"></a>
- `docs/customer-topup-ui-20261016.md`

<a id="f-532ffe68ba6f"></a>
- `docs/deployment-20261013-supplier-sms.md`

<a id="f-850a97d2c22c"></a>
- `docs/evidence/epay-upgrade-callback-20261017/README.md`

<a id="f-e353ba102176"></a>
- `docs/evidence/epay-upgrade-callback-20261017/barrier-before.log`

<a id="f-223c94572d02"></a>
- `docs/evidence/epay-upgrade-callback-20261017/hold-after.log`

<a id="f-ca0ca61c940e"></a>
- `docs/evidence/epay-upgrade-callback-20261017/integrated-full/result.json`

<a id="f-33a04168e63b"></a>
- `docs/evidence/epay-upgrade-callback-20261017/integrated-full/test.log`

<a id="f-d9d1dbe10ef4"></a>
- `docs/evidence/epay-upgrade-callback-20261017/integrated-race/result.json`

<a id="f-738eada9f810"></a>
- `docs/evidence/epay-upgrade-callback-20261017/integrated-race/test.log`

<a id="f-e9488aba78d9"></a>
- `docs/evidence/epay-upgrade-callback-20261017/integrated-static/result.json`

<a id="f-8b8734cdc28a"></a>
- `docs/evidence/epay-upgrade-callback-20261017/integrated-static/test.log`

<a id="f-10cab920b4b6"></a>
- `docs/evidence/epay-upgrade-callback-20261017/original-upgrade-before.log`

<a id="f-64a4ac1d5b15"></a>
- `docs/evidence/epay-upgrade-callback-20261017/race-after.log`

<a id="f-6a3be729335a"></a>
- `docs/evidence/epay-upgrade-callback-20261017/refund-migrations-after.log`

<a id="f-1ad999d3ed5e"></a>
- `docs/evidence/epay-upgrade-callback-20261017/scenarios-after.log`

<a id="f-b04eead6c9fa"></a>
- `docs/evidence/epay-upgrade-callback-20261017/shared-settlement-after.log`

<a id="f-42ce69382434"></a>
- `docs/evidence/g09-cohort-20261017/combined/build.log`

<a id="f-2cf193f02360"></a>
- `docs/evidence/g09-cohort-20261017/combined/diff.log`

<a id="f-99a29829a00b"></a>
- `docs/evidence/g09-cohort-20261017/combined/fixture-race.log`

<a id="f-afce9da6c031"></a>
- `docs/evidence/g09-cohort-20261017/combined/http-race.log`

<a id="f-83b8e7787d87"></a>
- `docs/evidence/g09-cohort-20261017/combined/index.json`

<a id="f-7b6545fa0ca0"></a>
- `docs/evidence/g09-cohort-20261017/combined/node.log`

<a id="f-962e80cb5841"></a>
- `docs/evidence/g09-cohort-20261017/combined/result.json`

<a id="f-0faf158e1538"></a>
- `docs/evidence/g09-cohort-20261017/combined/vet.log`

<a id="f-63546a915564"></a>
- `docs/evidence/g09-cohort-20261017/run1/boundaries.json`

<a id="f-9055b1036383"></a>
- `docs/evidence/g09-cohort-20261017/run1/followup.json`

<a id="f-15a0e5837bdd"></a>
- `docs/evidence/g09-cohort-20261017/run1/index.json`

<a id="f-e342e421c173"></a>
- `docs/evidence/g09-cohort-20261017/run1/result.json`

<a id="f-297dd917cdec"></a>
- `docs/evidence/g09-cohort-20261017/run1/test.log`

<a id="f-7436ac761e73"></a>
- `docs/evidence/g09-cohort-20261017/run2/boundaries.json`

<a id="f-d6e19ec74b4d"></a>
- `docs/evidence/g09-cohort-20261017/run2/failure-projection.json`

<a id="f-b87b9de2de0c"></a>
- `docs/evidence/g09-cohort-20261017/run2/index.json`

<a id="f-69b3bf2ccace"></a>
- `docs/evidence/g09-cohort-20261017/run2/result.json`

<a id="f-7c92b7a82a7b"></a>
- `docs/evidence/g09-cohort-20261017/run2/test.log`

<a id="f-eb8dcb376c51"></a>
- `docs/evidence/g09-cohort-20261017/run3/boundaries.json`

<a id="f-a0de37fc10c6"></a>
- `docs/evidence/g09-cohort-20261017/run3/index.json`

<a id="f-da5a97ae7b9d"></a>
- `docs/evidence/g09-cohort-20261017/run3/parameter-type-probe.txt`

<a id="f-340849eeb76c"></a>
- `docs/evidence/g09-cohort-20261017/run3/result.json`

<a id="f-0ae9b554fb24"></a>
- `docs/evidence/g09-cohort-20261017/run3/test.log`

<a id="f-14e46d5134c8"></a>
- `docs/evidence/g09-cohort-20261017/run4/boundaries.json`

<a id="f-49152be5e5fb"></a>
- `docs/evidence/g09-cohort-20261017/run4/index.json`

<a id="f-78756ac42d99"></a>
- `docs/evidence/g09-cohort-20261017/run4/result.json`

<a id="f-2a54186daaa5"></a>
- `docs/evidence/g09-cohort-20261017/run4/test.log`

<a id="f-8e7b6c520411"></a>
- `docs/evidence/g09-epay-scenario-20261017/node-guards.log`

<a id="f-8adfc97c7e7d"></a>
- `docs/evidence/g09-epay-scenario-20261017/real-pg/result.json`

<a id="f-486bc370dfef"></a>
- `docs/evidence/g09-epay-scenario-20261017/real-pg/test.log`

<a id="f-27d9cf1f8285"></a>
- `docs/evidence/g09-pg-observer-20261017/no-pg-concurrency-race.txt`

<a id="f-54efc6081dd2"></a>
- `docs/evidence/g09-pg-observer-20261017/no-pg-test.txt`

<a id="f-c56de080aaff"></a>
- `docs/evidence/g09-pg-observer-20261017/real-pg/result.json`

<a id="f-03d338ce99d7"></a>
- `docs/evidence/g09-pg-observer-20261017/real-pg/test.log`

<a id="f-2d742dd69588"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/build-run1/failure.log`

<a id="f-09f2d270ac5b"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/build-run1/preflight.json`

<a id="f-145bea1f08d1"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/build-run2/build.log`

<a id="f-aaa543c612b8"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/build-run2/preflight.json`

<a id="f-bf86d0c61523"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/build-run2/result.json`

<a id="f-0280c51e0548"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/build-run3/inputs.json`

<a id="f-3727d7ff8ceb"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/build-run3/result.json`

<a id="f-736daf2d4af3"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/checks/fixture-guards-race.log`

<a id="f-07cc153ea7dc"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/checks/node-contract-183e7a1.log`

<a id="f-d7281cd99450"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/checks/node-regression.log`

<a id="f-d9d5ebb67ede"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/checks/ordinary-process-183e7a1.log`

<a id="f-51053128fe07"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/index.json`

<a id="f-86738311b8b2"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/preflight/process-probe.log`

<a id="f-5aec126cc405"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/preflight/runtime-budget.json`

<a id="f-6c4c688599c3"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/baseline.json`

<a id="f-7030804f3afd"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/cache-mount-fixed-negative-probe.json`

<a id="f-ad21c7eed006"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/cache-mount-negative-probe.json`

<a id="f-f7a95d22d3d5"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/driver.log`

<a id="f-a727ea93e6ff"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/driver.txt`

<a id="f-87cccd396923"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/final-gates.json`

<a id="f-d5ba34e1e38f"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/host-fixture.log`

<a id="f-d02dcdf762dc"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/inputs.json`

<a id="f-1562c10f0e15"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/preflight.json`

<a id="f-f2ad86a24ccf"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run1/report.json`

<a id="f-d97b54b40ea8"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/command-1.json`

<a id="f-0ef9d8709e32"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/command-2.json`

<a id="f-21fe8e0d8b3d"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/command-3.json`

<a id="f-cd5d866aaa96"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/command-4.json`

<a id="f-a8ed627ecb59"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/command-5.json`

<a id="f-8a50107280b6"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/command-6.json`

<a id="f-0ca94dd93b9c"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/done.json`

<a id="f-a209f1e80ed0"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/result-1.json`

<a id="f-4228fd65013e"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/result-2.json`

<a id="f-b6a62bb724ff"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/result-3.json`

<a id="f-8657257bb6c2"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/result-4.json`

<a id="f-cd7cc7886fe3"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/result-5.json`

<a id="f-1d85c138a218"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/result-6.json`

<a id="f-239a02e96b2c"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/control/schema-ready.json`

<a id="f-e013af105bbe"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/driver.txt`

<a id="f-cb958d8504f1"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/final-gates.json`

<a id="f-5d5d5a66cd30"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/inputs.json`

<a id="f-da3d6f83a4a7"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/preflight.json`

<a id="f-165e0bbc294b"></a>
- `docs/evidence/g09-runtime-bootstrap-20261017/runtime-run2/report.json`

<a id="f-28812c151df1"></a>
- `docs/evidence/g09-sampler-window-20261017/combined-tools/result.json`

<a id="f-af772fb5f63b"></a>
- `docs/evidence/g09-sampler-window-20261017/combined-tools/test.log`

<a id="f-ac2a4877e91d"></a>
- `docs/evidence/g09-sampler-window-20261017/node-test-final-slot-too-short-fail.txt`

<a id="f-f48148c10cf7"></a>
- `docs/evidence/g09-sampler-window-20261017/node-test-pre-boundary-review-16-pass.txt`

<a id="f-25f81d6dabc1"></a>
- `docs/evidence/g09-sampler-window-20261017/node-test.txt`

<a id="f-b70e53059698"></a>
- `docs/evidence/g09-sampler-window-20261017/parent-review/result.json`

<a id="f-381d4474f37a"></a>
- `docs/evidence/g09-sampler-window-20261017/parent-review/test.log`

<a id="f-2792f21e0f08"></a>
- `docs/evidence/g09-supplier-sink-20261017/resource-bound/result.json`

<a id="f-ad0a97a6a5fd"></a>
- `docs/evidence/g09-supplier-sink-20261017/resource-bound/test.log`

<a id="f-d2bfc954b5d5"></a>
- `docs/evidence/g09-supplier-sink-20261017/result.json`

<a id="f-b9e3c486fbbf"></a>
- `docs/evidence/g09-supplier-sink-20261017/test.log`

<a id="f-808b11d2c4fc"></a>
- `docs/evidence/g09-supplier-worker-smoke-20261017/main-integration/result.json`

<a id="f-be3f46f91205"></a>
- `docs/evidence/g09-supplier-worker-smoke-20261017/main-integration/test.log`

<a id="f-b1c76feac26c"></a>
- `docs/evidence/g09-supplier-worker-smoke-20261017/no-pg-validation.json`

<a id="f-92e6c95470f4"></a>
- `docs/evidence/g09-supplier-worker-smoke-20261017/real-pg/result.json`

<a id="f-d0b6e2ddad3a"></a>
- `docs/evidence/g09-supplier-worker-smoke-20261017/real-pg/test.log`

<a id="f-a686ee51cfc6"></a>
- `docs/evidence/g09-tooling-20261017/check-results.json`

<a id="f-7b47dd1ed400"></a>
- `docs/evidence/g09-tooling-20261017/go-protocol-race.log`

<a id="f-437d92d8b0b9"></a>
- `docs/evidence/g09-tooling-20261017/go-protocol-vet.log`

<a id="f-b1939ee9c861"></a>
- `docs/evidence/g09-tooling-20261017/merged-helpers/1-node.log`

<a id="f-6126f7bb794d"></a>
- `docs/evidence/g09-tooling-20261017/merged-helpers/2-fixture-race.log`

<a id="f-3a24a645416b"></a>
- `docs/evidence/g09-tooling-20261017/merged-helpers/3-go-build.log`

<a id="f-65f541181927"></a>
- `docs/evidence/g09-tooling-20261017/merged-helpers/4-go-vet.log`

<a id="f-63e1e2146ca0"></a>
- `docs/evidence/g09-tooling-20261017/merged-helpers/5-diff-check.log`

<a id="f-cda8051cb057"></a>
- `docs/evidence/g09-tooling-20261017/merged-helpers/result.json`

<a id="f-d85549e2204b"></a>
- `docs/evidence/g09-tooling-20261017/node-syntax.log`

<a id="f-9b905f025161"></a>
- `docs/evidence/g09-tooling-20261017/node-tests.log`

<a id="f-9b0031a8abc9"></a>
- `docs/evidence/g09-tooling-20261017/process-sampler-increment/node-tests.log`

<a id="f-0edea836a1fc"></a>
- `docs/evidence/g09-tooling-20261017/process-sampler-increment/result.json`

<a id="f-d68c24545ade"></a>
- `docs/evidence/g09-tooling-20261017/report-guard-increment/check-results.json`

<a id="f-5a3c30804974"></a>
- `docs/evidence/g09-tooling-20261017/report-guard-increment/go-protocol-race.log`

<a id="f-89c0fbda3759"></a>
- `docs/evidence/g09-tooling-20261017/report-guard-increment/go-protocol-vet.log`

<a id="f-7123c4e39ee4"></a>
- `docs/evidence/g09-tooling-20261017/report-guard-increment/node-syntax.log`

<a id="f-c2ff94bba6ca"></a>
- `docs/evidence/g09-tooling-20261017/report-guard-increment/node-tests.log`

<a id="f-cfb245202837"></a>
- `docs/evidence/g09-tooling-20261017/report-guard-increment/source-index.json`

<a id="f-61fcdfbac022"></a>
- `docs/evidence/g09-tooling-20261017/source-api-pg-smoke/result.json`

<a id="f-dc254be1cf78"></a>
- `docs/evidence/g09-tooling-20261017/source-api-pg-smoke/test.log`

<a id="f-14fb178e9528"></a>
- `docs/evidence/g09-tooling-20261017/source-index.json`

<a id="f-8b64cdf70fa2"></a>
- `docs/evidence/g09-tooling-20261017/source-worker-fixed-pg-smoke/result.json`

<a id="f-67a8680d61cf"></a>
- `docs/evidence/g09-tooling-20261017/source-worker-fixed-pg-smoke/test.log`

<a id="f-3bfad583628f"></a>
- `docs/evidence/g09-tooling-20261017/source-worker-pg-smoke/result.json`

<a id="f-99f874798297"></a>
- `docs/evidence/g09-tooling-20261017/source-worker-pg-smoke/test.log`

<a id="f-867fd03ff36b"></a>
- `docs/evidence/local-renewal-refund-20261017/browser-fixture-cleanup.json`

<a id="f-7ce68ed057e0"></a>
- `docs/evidence/local-renewal-refund-20261017/browser-web-cleanup.json`

<a id="f-0e8416c9a1b1"></a>
- `docs/evidence/local-renewal-refund-20261017/build-cleanup.json`

<a id="f-938df3de2c8e"></a>
- `docs/evidence/local-renewal-refund-20261017/db-after-confirm.json`

<a id="f-e306688930da"></a>
- `docs/evidence/local-renewal-refund-20261017/db-after-reject.json`

<a id="f-276d6c5c8b23"></a>
- `docs/evidence/local-renewal-refund-20261017/db-before-confirm.json`

<a id="f-437c6a237ec5"></a>
- `docs/evidence/local-renewal-refund-20261017/first-effect-race.log`

<a id="f-da68c796d29d"></a>
- `docs/evidence/local-renewal-refund-20261017/full-suite.log`

<a id="f-9f6612d1ec39"></a>
- `docs/evidence/local-renewal-refund-20261017/go-build.log`

<a id="f-1b227d82e3cb"></a>
- `docs/evidence/local-renewal-refund-20261017/go-vet.log`

<a id="f-fd761a540a04"></a>
- `docs/evidence/local-renewal-refund-20261017/http-race.log`

<a id="f-731e02891686"></a>
- `docs/evidence/local-renewal-refund-20261017/pagination-seed.json`

<a id="f-14455a396317"></a>
- `docs/evidence/local-renewal-refund-20261017/restore.log`

<a id="f-e9c345c5c878"></a>
- `docs/evidence/local-renewal-refund-20261017/serial-fixed-full-suite-result.json`

<a id="f-e7c0ddff30f4"></a>
- `docs/evidence/local-renewal-refund-20261017/serial-fixed-full-suite.log`

<a id="f-2d9c386c7372"></a>
- `docs/evidence/local-renewal-refund-20261017/serial-full-suite-result.json`

<a id="f-4a7267ec76fe"></a>
- `docs/evidence/local-renewal-refund-20261017/serial-full-suite.log`

<a id="f-59252db43cbc"></a>
- `docs/evidence/local-renewal-refund-20261017/store-race.log`

<a id="f-42cdeaa547e0"></a>
- `docs/evidence/local-renewal-refund-20261017/upgrade.log`

<a id="f-533cb4481fae"></a>
- `docs/evidence/local-renewal-refund-20261017/web-build.log`

<a id="f-741091fd714a"></a>
- `docs/evidence/local-renewal-refund-20261017/web-lint.log`

<a id="f-c6f792d83f98"></a>
- `docs/evidence/local-renewal-refund-20261017/web-typecheck.log`

<a id="f-0ba551f59198"></a>
- `docs/evidence/release-dual-web-20261017/a-build-anchor.json`

<a id="f-88dc9529d68b"></a>
- `docs/evidence/release-dual-web-20261017/a-resource-observations.json`

<a id="f-bdc32ac2717a"></a>
- `docs/evidence/release-dual-web-20261017/a-v3-build-anchor.json`

<a id="f-e45e70129583"></a>
- `docs/evidence/release-dual-web-20261017/b-build-anchor.json`

<a id="f-91c70569ccde"></a>
- `docs/evidence/release-dual-web-20261017/b-resource-observations.json`

<a id="f-f573c72e22fd"></a>
- `docs/evidence/release-dual-web-20261017/b-v3-build-anchor.json`

<a id="f-a63550facd22"></a>
- `docs/evidence/release-dual-web-20261017/historical-candidate-cleanup-summary.json`

<a id="f-458c3cb64b44"></a>
- `docs/evidence/release-dual-web-20261017/input-evidence-index.json`

<a id="f-10ff0faacf50"></a>
- `docs/evidence/release-dual-web-20261017/run1-5-evidence-verification.json`

<a id="f-8084277af683"></a>
- `docs/evidence/release-dual-web-20261017/run1-summary.json`

<a id="f-3393c954b7ae"></a>
- `docs/evidence/release-dual-web-20261017/run2-summary.json`

<a id="f-8c20ca97fc67"></a>
- `docs/evidence/release-dual-web-20261017/run3-summary.json`

<a id="f-b47b72535abc"></a>
- `docs/evidence/release-dual-web-20261017/run4-summary.json`

<a id="f-c785f89124a6"></a>
- `docs/evidence/release-dual-web-20261017/run5-summary.json`

<a id="f-7dc582c40534"></a>
- `docs/evidence/release-dual-web-20261017/run6-build-and-test-index.json`

<a id="f-a15313920765"></a>
- `docs/evidence/release-dual-web-20261017/run6-summary.json`

<a id="f-2bd6833e48ec"></a>
- `docs/evidence/release-runtime-20261017/candidate3-anchor.json`

<a id="f-07a4cdbcb519"></a>
- `docs/evidence/release-runtime-20261017/local-archive-index.json`

<a id="f-365b8894d5b5"></a>
- `docs/evidence/release-runtime-20261017/primary-cleanup-check.json`

<a id="f-14ab8f46fdc9"></a>
- `docs/evidence/release-runtime-20261017/run3-report.json`

<a id="f-340a756f4a35"></a>
- `docs/evidence/release-runtime-20261017/run4-report.json`

<a id="f-af69db7927a1"></a>
- `docs/evidence/release-runtime-20261017/run4-resource-observations.json`

<a id="f-2ff09ede6ee6"></a>
- `docs/evidence/release-static-retention-20261017/attempts.json`

<a id="f-473dcac35a9e"></a>
- `docs/evidence/release-static-retention-20261017/run5-resource-observations.json`

<a id="f-bea94412955b"></a>
- `docs/evidence/release-static-retention-20261017/run5-summary.json`

<a id="f-6692cbe5b66d"></a>
- `docs/evidence/worker-sigkill-20261017/cleanup.json`

<a id="f-44add41ce79e"></a>
- `docs/evidence/worker-sigkill-20261017/contract-race.log`

<a id="f-638843a63f3e"></a>
- `docs/evidence/worker-sigkill-20261017/gate-no-dsn.log`

<a id="f-82bc56a6fc69"></a>
- `docs/evidence/worker-sigkill-20261017/gate-no-matching-suites.log`

<a id="f-935a2a4721f1"></a>
- `docs/evidence/worker-sigkill-20261017/index.json`

<a id="f-7eb0e866d990"></a>
- `docs/evidence/worker-sigkill-20261017/integrated-race.log`

<a id="f-6d392e02e96c"></a>
- `docs/evidence/worker-sigkill-20261017/vet.log`

<a id="f-c21eda60f7db"></a>
- `docs/external-refund-acceptance-20261016.md`

<a id="f-def7dd46878c"></a>
- `docs/external-refund-browser-evidence-20261016.json`

<a id="f-bc1dd0726201"></a>
- `docs/external-refund-http-acceptance-20261016.md`

<a id="f-5bfe6b3bd57e"></a>
- `docs/external-refund-reversal-backend-acceptance-20261016.md`

<a id="f-01a64d0b1fd2"></a>
- `docs/external-refund-reversal-browser-acceptance-20261016.md`

<a id="f-f8835292a08b"></a>
- `docs/external-refund-store-acceptance-20261016.md`

<a id="f-40cbaad818e8"></a>
- `docs/g09-capacity-allocator-increment-20261017.md`

<a id="f-7322808c5ce7"></a>
- `docs/g09-capacity-controller-increment-20261017.md`

<a id="f-7ebd74ce72f0"></a>
- `docs/g09-capacity-drain-plan-20261017.md`

<a id="f-0b4b0f96792b"></a>
- `docs/g09-capacity-fixture-protocol-increment-20261017.md`

<a id="f-8ac83382c64a"></a>
- `docs/g09-capacity-profile-validator-increment-20261017.md`

<a id="f-3de7c3a04bdb"></a>
- `docs/g09-cohort-acceptance-20261017.md`

<a id="f-2f482669641f"></a>
- `docs/g09-cohort-independent-review-20261017.md`

<a id="f-086ea84125f4"></a>
- `docs/g09-cohort-source-worker-increment-20261017.md`

<a id="f-9469d50cfe33"></a>
- `docs/g09-cohort-verifier-increment-20261017.md`

<a id="f-45afde5e2a68"></a>
- `docs/g09-epay-scenario-increment-20261017.md`

<a id="f-68682d98ab5a"></a>
- `docs/g09-fixture-contract-20261017.md`

<a id="f-b61d94ab4da1"></a>
- `docs/g09-pg-observer-increment-20261017.md`

<a id="f-ac950b25421b"></a>
- `docs/g09-readonly-projection.md`

<a id="f-28fa790b07b3"></a>
- `docs/g09-runtime-bootstrap-acceptance-20261017.md`

<a id="f-a70bc9457d08"></a>
- `docs/g09-sampler-window-increment-20261017.md`

<a id="f-d431df4ebe45"></a>
- `docs/g09-source-worker-smoke-20261017.md`

<a id="f-50dec5ac2195"></a>
- `docs/g09-supplier-sink-increment-20261017.md`

<a id="f-424745085d28"></a>
- `docs/g09-supplier-worker-smoke-20261017.md`

<a id="f-9655cab31fba"></a>
- `docs/g09-tooling-increment-20261017.md`

<a id="f-22f1f9bbee28"></a>
- `docs/generated/close-expired-recovery-browser-acceptance-20261017.json`

<a id="f-7cb5c998b765"></a>
- `docs/generated/external-refund-reversal-browser-acceptance-20261016.json`

<a id="f-03c6d1a88dc2"></a>
- `docs/generated/live-web-build-recovery-20261017.json`

<a id="f-faaa5d84462c"></a>
- `docs/generated/local-renewal-refund-browser-acceptance-20261017.json`

<a id="f-a2af8b97feb6"></a>
- `docs/generated/order-external-refund-browser-acceptance-20261017.json`

<a id="f-9dbf2e428d03"></a>
- `docs/generated/payment-anomaly-resolution-browser-acceptance-20261017.json`

<a id="f-423182f02157"></a>
- `docs/generated/payment-close-browser-acceptance-20261016.json`

<a id="f-1a8f9a7dcc56"></a>
- `docs/generated/payment-close-verification-browser-acceptance-20261017.json`

<a id="f-7328a470e080"></a>
- `docs/generated/release-preparation-integration-20261017.json`

<a id="f-e6861ed7a187"></a>
- `docs/generated/resolved-topup-external-refund-browser-acceptance-20261017.json`

<a id="f-7534a577df53"></a>
- `docs/legacy-login-renewal-certification-20261016.md`

<a id="f-6836dc177d91"></a>
- `docs/legacy-supplier-lifecycle-review-20261013.md`

<a id="f-561d7264f02b"></a>
- `docs/live-web-build-recovery-20261017.md`

<a id="f-bb9fd8f81286"></a>
- `docs/local-renewal-refund-acceptance-20261017.md`

<a id="f-3f3cfb23fe61"></a>
- `docs/local-renewal-refund-independent-review-20261017.md`

<a id="f-f54b5f253356"></a>
- `docs/migration-reconciliation-20261014.md`

<a id="f-fb0086507c2b"></a>
- `docs/mpay-merchant-protocol-boundary-20261016.md`

<a id="f-07ff0929e4ff"></a>
- `docs/native-core-acceptance.md`

<a id="f-bed79fc58978"></a>
- `docs/native-security-acceptance.md`

<a id="f-0546e04e5252"></a>
- `docs/nokvm-monitoring-acceptance.md`

<a id="f-3f1c10befcf4"></a>
- `docs/nokvm-storage-acceptance.md`

<a id="f-2fd845820ef4"></a>
- `docs/notification-closure-acceptance-20261013.md`

<a id="f-9d2b3596b3a9"></a>
- `docs/notification-scheduler-acceptance.md`

<a id="f-3a22b0f56dc8"></a>
- `docs/notification-worker-sigkill-acceptance-20261017.md`

<a id="f-a73a0cdfb751"></a>
- `docs/order-external-refund-acceptance-20261017.md`

<a id="f-c1bc93b6926a"></a>
- `docs/order-external-refund-browser-acceptance-20261017.md`

<a id="f-3a5a9cd70b51"></a>
- `docs/overdue-scan-progress-acceptance-20261016.md`

<a id="f-5e7c06e195e0"></a>
- `docs/parallel-development.md`

<a id="f-9e373b30dd2b"></a>
- `docs/payment-anomaly-refund-backend-acceptance-20261017.md`

<a id="f-de60d377ff60"></a>
- `docs/payment-close-browser-acceptance-20261016.md`

<a id="f-1237426628a9"></a>
- `docs/payment-close-unlock-backend-acceptance-20261016.md`

<a id="f-7a764f2a39fe"></a>
- `docs/payment-close-verification-backend-acceptance-20261017.md`

<a id="f-4df09c3b53ce"></a>
- `docs/payment-close-verification-design-20261016.md`

<a id="f-6162d75819e7"></a>
- `docs/payment-exception-browser-fixture-20261016.md`

<a id="f-cb602c83486e"></a>
- `docs/payment-exception-resolution-browser-acceptance-20261017.md`

<a id="f-d26a8e1be382"></a>
- `docs/payment-exceptions-integration-acceptance-20261016.md`

<a id="f-cb6a53f746eb"></a>
- `docs/payment-expiry-acceptance.md`

<a id="f-49b2873e254c"></a>
- `docs/product-upgrade-browser-evidence-20261016.json`

<a id="f-b0e71fbed259"></a>
- `docs/product-upgrade-restoration-acceptance-20261016.md`

<a id="f-2f1ba66d2620"></a>
- `docs/product-upgrade-restoration-test-20261016.md`

<a id="f-44c0dd09ca11"></a>
- `docs/product-upgrade-ui-20261016.md`

<a id="f-11554f6d96b2"></a>
- `docs/promo-code-acceptance-20261016.md`

<a id="f-495588286a62"></a>
- `docs/promo-code-browser-evidence-20261016.json`

<a id="f-742161118979"></a>
- `docs/promo-code-ui-20261016.md`

<a id="f-e828c20a99d5"></a>
- `docs/promo-restoration-api-test-20261016.md`

<a id="f-3e9048caa174"></a>
- `docs/real-payment-acceptance-20261013.md`

<a id="f-6f4bd7b52d42"></a>
- `docs/real-provider-p0-acceptance-20261013.md`

<a id="f-837a7ee85e1d"></a>
- `docs/real-sms-acceptance-20261013.md`

<a id="f-d0cec8d3c11a"></a>
- `docs/reconstruction.md`

<a id="f-6f17db737d20"></a>
- `docs/release-dual-web-acceptance-20261017.md`

<a id="f-9eea5958b7e3"></a>
- `docs/release-preparation-integration-acceptance-20261017.md`

<a id="f-7842492242e4"></a>
- `docs/release-runtime-integration-acceptance-20261017.md`

<a id="f-75ad468702e2"></a>
- `docs/release-runtime-real-build-acceptance-20261017.md`

<a id="f-a39702ab1391"></a>
- `docs/release-static-retention-acceptance-20261017.md`

<a id="f-e27ef7af0553"></a>
- `docs/replacement-gap-audit-20261016.md`

<a id="f-9e90facb6308"></a>
- `docs/resolved-topup-external-refund-acceptance-20261017.md`

<a id="f-26b48b38a01d"></a>
- `docs/resolved-topup-external-refund-browser-acceptance-20261017.md`

<a id="f-8c06b4a33b88"></a>
- `docs/restore-evidence-scope-acceptance-20261016.md`

<a id="f-0e506c329191"></a>
- `docs/review-fixes-20261015.md`

<a id="f-ee600faf9a2c"></a>
- `docs/review-fixes-20261016.md`

<a id="f-d031c198ddb5"></a>
- `docs/schema153-representative-backup-restore-20261017.md`

<a id="f-b8356ac35511"></a>
- `docs/supplier-acceptance.md`

<a id="f-8acc1c8a4325"></a>
- `docs/supplier-customer-acceptance-20261016.md`

<a id="f-d778f6e18f52"></a>
- `docs/supplier-customer-access-20261016.md`

<a id="f-e7150c6d7087"></a>
- `docs/supplier-customer-browser-evidence-20261016.json`

<a id="f-61e6af002c03"></a>
- `docs/supplier-customer-ui-20261016.md`

<a id="f-f2eac220df6f"></a>
- `docs/supplier-renewal-acceptance-20261013.md`

<a id="f-1fa4fed8401c"></a>
- `docs/supplier-unattended-acceptance-20261013.md`

<a id="f-60e65ea15281"></a>
- `docs/supplier-worker-acceptance-20261016.md`

<a id="f-57df1260f406"></a>
- `docs/supplier-worker-sigkill-acceptance-20261017.md`

<a id="f-2f8b1d039ea7"></a>
- `docs/ticket-notification-acceptance-20261016.md`

<a id="f-344d10b4ec84"></a>
- `docs/ticket-notification-browser-evidence-20261016.json`

<a id="f-0fe8dbf291d0"></a>
- `docs/ticket-notification-ui-20261016.md`

<a id="f-8caaf46ea7b0"></a>
- `docs/topup-liability-browser-evidence-20261016.json`

<a id="f-1f4848ba0bb3"></a>
- `docs/topup-liability-http-test-20261016.md`

<a id="f-c2807dad8f80"></a>
- `docs/topup-liability-resolution-acceptance-20261016.md`

<a id="f-9f0a2fbb3d5a"></a>
- `docs/worker-sigkill-acceptance-20261017.md`

<a id="f-a2b1cd1a8d61"></a>
- `internal/g09fixture/capacity_fixture.go`

<a id="f-9d0c8c29e1e9"></a>
- `internal/g09fixture/capacity_fixture_test.go`

<a id="f-236fcf2acd10"></a>
- `internal/g09projection/projection.go`

<a id="f-0649f33b12a3"></a>
- `internal/g09projection/projection_test.go`

<a id="f-c7a86efaf586"></a>
- `scripts/accept-g09-capacity.mjs`

<a id="f-4402a0e508b5"></a>
- `scripts/g09-capacity-allocator.mjs`

<a id="f-af774465ae8d"></a>
- `scripts/g09-capacity-allocator.test.mjs`

<a id="f-dd2ab5e62e64"></a>
- `scripts/g09-capacity-contract.mjs`

<a id="f-76886a94e353"></a>
- `scripts/g09-capacity-contract.test.mjs`

<a id="f-1b311dbd365f"></a>
- `scripts/g09-capacity-controller.mjs`

<a id="f-88ab00f55a74"></a>
- `scripts/g09-capacity-controller.test.mjs`

<a id="f-ab15693d2768"></a>
- `scripts/g09-capacity-projection.mjs`

<a id="f-55fd31aab9c5"></a>
- `scripts/g09-sampler-window.mjs`

<a id="f-c8abfd66747e"></a>
- `scripts/g09-sampler-window.test.mjs`

<a id="f-4fa398260bb6"></a>
- `docs/generated/route-coverage.json`

<a id="f-ec7f78266753"></a>
- `docs/generated/admin-page-contracts.json`
