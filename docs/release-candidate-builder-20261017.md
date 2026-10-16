# 本地候选构建器与清单核验（2026-10-17）

状态：G04首个候选准备增量，非发布/部署；G04/G09仍未关闭。本实现不改Go业务、数据库、服务、代理或主仓 `.next`，不运行全量Go回归。真实完整候选构建待主代理代码审查后在独立目录执行，本记录不把fixture当真实构建。

## 使用

从包含本脚本的已提交、干净源码worktree执行；被选提交也必须包含完全相同的构建器文件。源目录有任何已跟踪修改或非忽略未跟踪文件（包括mimosa输出）均拒绝，先选择干净独立worktree，不删除其他人的工作。不将任意已有 `.next` 接收为“同源构建”。

```bash
candidate_commit='<已审核完整提交SHA>'
node scripts/build-release-candidate.mjs \
  --source "$PWD" --commit "$candidate_commit" \
  --output /tmp/zjmfgo-release-candidate-20261017 \
  --shared-deps /www/wwwroot/zjmfgo/web/node_modules \
  --api-internal-url http://127.0.0.1:8088
# 保存上一步最终JSON中的manifestSHA256到独立受控记录，勿只放在候选目录。
node scripts/verify-release-candidate.mjs \
  /tmp/zjmfgo-release-candidate-20261017 '<独立保存的manifestSHA256>' "$PWD"
node --test scripts/release-candidate.test.mjs
```

输出路径必须是尚不存在的 `/tmp/` 目录或源仓库忽略的 `output/` 子目录，拒绝越界、覆盖已有目录和软链祖先。成功产物留存本地，不提交Git、不创建tar；失败只删除本次独占创建的候选目录。源源码、共享依赖和主仓 `.next` 不删除、不覆盖。构建器只创建一个新checkout，完成后迁移/模板少量复制，Web `.next` 用rename移交，无依赖副本；独立checkout退出即清理。磁盘不足构建失败，不自动清理其他产物。

## 真实来源与清单

构建器以Git固定commit/tree，在新空目录 `git clone --shared --no-checkout`、detach到commit，再核对全体跟踪文件SHA256。Git共享的是只读对象存储，不是源工作区文件。ignored `.env`、`.next`、业务数据和非跟踪输出不会进入新checkout；受保护已跟踪 `.env`/密钥扩展名/软链/submodule拒绝。已跟踪根 `.env.example`只作为源码模板摘要，不进入候选。此检查不构成任意代码/嵌入秘密的通用审计，仍需可信审核提交。

构建环境只传PATH/HOME/LANG和明确API origin、production/telemetry开关，不继承Cookie、DSN、支付/供应商密钥、NODE_OPTIONS、额外Next环境变量或源 `.env`；API必须HTTP(S) origin，拒绝userinfo、路径、query与fragment。Go依赖通过原 `scripts/go.sh`、`-mod=readonly -trimpath -buildvcs=true`构建api/worker/migrate；检查build info的revision和modified=false。Web通过 `npm run build -- --webpack`构建，保留prebuild运行目录保护，绝不在源Web目录执行build。

`manifest.json`记录：

- 固定commit/tree和每个已跟踪源码文件SHA256/字节数；构建器格式版本、3个脚本摘要，实际Go/Node/npm/平台、构建命令；go.mod/go.sum/Web锁文件摘要。
- 三个二进制完整字节SHA、可执行位、实际Go build info。
- 全部数字编号SQL迁移列表与checksum、所选部署systemd/Next/构建模板摘要；复制迁移/模板从固定checkout取得，核验时与源摘要绑定。
- `.next/BUILD_ID`、Next版本、webpack、显式API origin，并从实际routes-manifest.json复核 `/api/:path*` rewrite目的地。
- **全部Web生产输出文件**：`web/.next`除构建缓存目录 `.next/cache`外的每个普通文件，及public、package/lock/Next配置；包含server/app、static和框架manifest，不按JS/CSS扩展名挑选。**明确不含可重建 `.next/cache`，不称整个原始 `.next`逐字节备份。** `.next`之外的共享node_modules不包含在包内。
- 全部候选普通文件路径、字节数、SHA和可执行位；拒绝所有候选软链、特殊文件、路径穿越及受保护环境/密钥文件。

构建完成后，不启动这个封存候选、不写缓存、不运行Next start后仍声称清单未变；任何预热/运行测试应另设可写运行目录并单列验证。多/少任何文件、字节或可执行位变化都会使verify失败，包括迁移、模板、BUILD_ID、chunk和清单本身。

## 共享依赖与信任界限

本批不npm ci/install、不复制node_modules、不chmod共享目录、不将其软链打进产物。要求提供真实且受控的依赖目录，其owner旁边的package-lock须与选定commit一致；完整依赖普通文件及内部软链目标的树摘要在build前后相同才封存，外逃依赖软链拒绝。清单记录复用方式、实际Next版本、依赖树摘要与文件数。

**owner锁相同、目录存在、甚至npm ls成功都不能证明每一安装文件由该lock可信安装。** before/after相同仅证明观测到的共享树未发生持久变化，不防攻击者临时篡改后恢复，也不是操作系统只读挂载。调用者须保证依赖owner可信、构建窗口无并发安装/修改；本脚本本身不写依赖。正式部署还需独立受控锁定安装/供应链验收。候选不含runtime依赖，不能当作独立离线运行发布包。

manifest不是签名。verify必须由操作者提供从构建结果另行保管的SHA256，防止攻击者同时改文件和目录内清单；可选可信clean源码仓库进一步比对Git commit/tree/每文件SHA。若操作者接受攻击者提供的清单及其SHA，不能据此证明真实性。摘要不能证明不受控外部 `.next` 来自某提交；只有可信构建器自行固定checkout执行并封存的链路提供这里的有限证据。工具链、共享依赖和构建主机可信性不由清单解决。

## 单元/夹具验证范围

`node --test scripts/release-candidate.test.mjs`使用真实临时Git仓库和文件：干净提交、源码dirty/未跟踪拒绝、受保护文件/源码软链、构建前拒绝且不创建候选、实际产物篡改/缺件/多文件、外部清单hash、重封fixture后的必要文件/SQL checksum/BUILD_ID/rewrite/模板/lock/二进制metadata错误、路径穿越/软链根与祖先/依赖逃逸、origin凭据拒绝。fixture二进制只是数据，不声称执行真实Go或Web构建。

真实候选构建、主线正式产物审查另记录commit、清单SHA、BUILD_ID、rewrite、磁盘占用和退出状态。旧chunk保留、真实代理HTTPS、三服务同包运行、PG18升级恢复、供应商/支付外部边界、切流切回、容量/崩溃恢复仍按[正式接管验收契约](release-readiness-next-contract-20261017.md)独立验收；本批没有关闭这些项目。
