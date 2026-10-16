# G04 可信 npm 依赖输入准备

本增量是依赖安装工具与小夹具验证，不是 runtime candidate、正式构建或部署。独立支线基线 `c1bf6f7`；不修改既有 v1/v2 builder/verifier。脚本默认 preflight；`--execute` 仅可作为最后一个参数，执行已由现有工作任务授权，不增加人工审批步骤。

最初只读检查两个已知 npm 日志目录共22条日志，没有找到可关联本项目的成功 npm ci/install 记录；既有共享 node_modules 约563MiB，仅目录哈希不能追补可信安装历史。已安装 Node 的 LICENSE 存在，但初始限定路径内没有找到官方发行归档/SHASUM或独立来源证明。因此工具不接纳未知共享安装，也不为其生成 trusted 标签。

主代理随后提供官方发行包验证的 Node v24.16.0、同包 npm11.13.0、LICENSE 与独立 SHA 锚。本支线实际只执行该工具链的 `--version` 和只读完整树校验，没有执行真实 npm ci。PATH npm 的11.16.0没有被用作安装输入。

```sh
node scripts/prepare-runtime-dependency-input.mjs \
  --source /absolute/clean/source --commit FULL_40_HEX_COMMIT \
  --output /tmp/NEW_EXCLUSIVE_DEPENDENCY_INPUT \
  --node /controlled/node/bin/node --node-license /controlled/node/LICENSE \
  --node-provenance /controlled/node.json --node-provenance-sha256 EXTERNAL_SHA256 \
  --npm-root /controlled/node/lib/node_modules/npm \
  --npm-cli /controlled/node/lib/node_modules/npm/bin/npm-cli.js \
  --npm-provenance /controlled/npm.json --npm-provenance-sha256 EXTERNAL_SHA256 \
  --registry https://registry.npmjs.org \
  --cache-bytes 157286400 --output-bytes 734003200 \
  --reserve-bytes 536870912 --max-peak-bytes 958398464 --max-seconds 1800
```

所有参数显式提供。`--execute` 才新建0700独占目录，并在私有空 install 子目录复制选定 Git 原始 package.json/package-lock.json，不复制工作树中的 ignored .npmrc/.env/node_modules；源 HEAD 必须等于完整提交且干净。npm 锁必须是v3，每个非根条目要求官方 HTTPS registry、无用户名/密码/query/fragment、SHA512 integrity；local/git/link依赖不接纳。当前锁553项通过只读检查。

Node证明沿用 [runtime builder](release-runtime-builder-20261017.md) 格式。npm证明必须由调用方基于独立可信官方输入建立，不能将未知安装的自述JSON当作来源证明：

```js
{
  format: 'zjmfgo-runtime-npm-provenance-v1',
  platform: 'linux/x64', nodeVersion: 'v24.16.0', version: '11.13.0',
  source: 'independently verified official Node archive and checksum record',
  inputTreeSHA256: sha256(JSON.stringify(files)),
  files // inventory(npmRoot, {dependencies:true}) 完整排序清单
}
```

调用方独立保留Node/npm JSON的SHA锚。脚本先校验锚、完整Node/LICENSE和npm树，再执行绝对路径 Node + 指定 npm-cli.js 的版本探测；安装前后重验工具与Git输入。实际命令为 `controlled-node absolute-npm-cli.js ci --registry https://registry.npmjs.org --include=dev --ignore-scripts --no-audit --no-fund`。依赖生命周期脚本禁用，当前包是否可在该模式下完成后续Next/native运行必须由真实安装/候选验收证明；本增量未提前宣称兼容。

安装环境从空对象建立，固定PATH为/usr/bin:/bin，HOME/TMPDIR/cache/user/global npmrc均在独占目录；不继承代理、NODE_OPTIONS/NODE_PATH、npm token或业务凭据。npm输出只在成功时保存受控日志与摘要；失败不回显任意npm输出。安装成功后记录真实命令/时间/Node/npm版本/平台、source commit、lock/package哈希、日志SHA、全树文件哈希及预算实测；仅在真实命令exit0且检查通过后写 builder兼容的dependencies.json。node_modules全文件/目录去写权限，去写前后完整inventory相同；0700控制不等于不可被root篡改，后续builder仍要求private只读mount和前后哈希。返回的provenanceSHA256须在目录外独立保留，不生成假装能自证的旁置锚。

安装总峰值958398464字节（914MiB）包含150MiB cache、700MiB依赖输出、64MiB辅助预算，另保留512MiB；这些是工程估计上限，不是测得真实峰值。安装命令每250ms检查自由空间/inode/总消耗，超时或超预算只结束自有进程组；cache/输出完成后再核对分项。轮询无法保证单次大写从不暂时越界，其他主机写入也可触发保守停止。失败仅在已创建目录的dev/inode仍符合独占所有权时回收它，既有输出、共享缓存、其他worktree或业务数据不清理。

实际只读preflight使用干净source `dec0f951e8d968203e133c96735bdfe99850d010` 的锁及主代理官方工具锚，通过PREFLIGHT_ONLY；可用2297974784字节、4850040 inode，安装峰值+reserve合计1495269376字节，预估余量802705408字节。没有新建输出目录或真实安装。该结果只说明当时安装预检有空间；后续1.2GiB构建/候选封装/运行副本/数据库日志必须按完整六项runtime预算另算，不能把本预算当作整个G04峰值或保证实际安装成功。

小fixture使用自有tiny shell模拟Node/npm，仅验证控制逻辑，不冒称真实npm安装：10项通过，覆盖registry/integrity、干净Git、版本锚/完整npm树、无写预检、环境隔离、既有路径保护、空间拒绝、只读封装及兼容既有builder、失败/超时回收。既有v1/v2 builder/verifier小fixture回归89项通过（含测试用微型Go二进制，未构建产品或Next）。未执行npm ci、Next完整构建、数据库、服务或真实网络安装。
