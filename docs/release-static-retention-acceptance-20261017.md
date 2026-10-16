# 真实 Nginx 静态资源保留隔离验收（2026-10-17）

状态：静态保留准备子项 PASS，完整双 Web 切换及 G04/G09 仍开放。按[最小契约](release-static-retention-contract-20261017.md)执行，不启动 Web/API/worker/迁移，不连接数据库，不部署、推送或重载宿主 Nginx。候选2只提供历史静态文件，不提升其缺少 SSR API origin 的 runtime 结论。

## 固定输入与实现

- 旧 candidate2：源码 `5118eac4d9d298574a6523af8f574381c5603ff2`，manifest 外锚 `c691d84bb79ccfb148a8668763d173f934973d9d0f3c5b2b25b546da39cac27a`。
- 新 candidate3：源码 `949ffe367f0bfac0567609d924386dfd17329871`，外锚 `5a4228c5223d01fe0bd7c0c951c106b70276055b78fa6ab6b0b6fb2b2415745a`；其完整同包运行证据见[前一批](release-runtime-real-build-acceptance-20261017.md)。
- 两包各220静态文件，共8585230字节；58个共用URL同字节，分别162个独有URL，并集382。每个资源保留来源提交/manifest外锚/path，共用项同时保留两份来源，实际provider优先新包。
- 独立分支 `1990eec` / `c3a4c48` 经主审以 `71e36f5` / `c7f2ce7` 合入；主代理实现运行器并修复预检和真实依赖问题。最终固定runner `500842708cb6424c75d48a98e3c152684425fede`，从干净独立worktree执行。
- `scripts/release-static-retention-lib.mjs`负责精确并集、冲突拒绝、真实文件类型/摘要核验和完整Nginx配置；`scripts/accept-release-static-retention.mjs`负责来源验证、只读staging、普通用户namespace、实际请求/重载和退出。
- 旧静态源验证单独认证外锚、有界独立manifest、干净Git HEAD完整sourceProof及全包runtimeInventory，不运行旧包或旧verifier，不接受旧launch。新包仍经当前完整runtime verifier。两包执行前后均重新认证。

真实目标 Nginx `1.30.4` SHA256 `5898eee290e64f5f5d3f38d424d2548326799baa11d6267c5f74ddba22796ae6`，不使用Node替代代理。Lua运行文件及显式host工具库jemalloc/luajit有摘要，35份实际二进制/库映射已核对；`/dev/zero (deleted)`仅作为Nginx匿名共享内存设备映射记录，其他无法读取的deleted文件仍拒绝。

## 隔离与实际结果

root控制器仅在自有私有mount namespace做bind+只读remount，再用setpriv将真实宿主UID/GID降为1000并清空附加组，进入bwrap私有mount/net/pid空间。外层`/proc`核对真实/有效UID/GID，内层再次核对1000和网络仅lo。只读映射两包static、官方Node、目标Nginx和已记录工具库；没有业务站点、配置、数据库socket或外部网络。

Nginx独立`-p/-c/-e`、daemon off，PID/日志/所有临时目录都在自有mutable；不include宿主配置。每次激活先真实`-t`、原子替换，再核对master PID、起始tick、exe摘要、uid/gid、参数及mount/net身份，只对该master发HUP。以实际generation响应和新worker身份证明接管。

| 项目 | 结果 |
| --- | --- |
| old → union → restored | 220 → 382 → 382，恢复配置继续保留新旧资源，未裁掉新URL |
| 全量GET/HEAD | 984组，GET长度/SHA匹配封存，HEAD无体且Content-Length/MIME/缓存/nosniff一致；无压缩替换 |
| 缓存 | 成功与304固定 `public, max-age=300, immutable`；所有被测错误为 `no-store`；安全query同字节，正确ETag→304、旧ETag→200原字节 |
| 持续旧URL | 131次，old19 / union67 / restored45；每次间隔为响应后10ms，记录请求起止时间和耗时；错误0、摘要变化0 |
| 原始协议反例 | 31项：点段/重复分隔/编码与双编码/未知、`.env`/服务端路径、畸形百分号/NUL和0x01、括号编码及不允许方法；预期400/404/405，不泄露私有路径 |
| 文件与配置夹具 | 同字节合并1项通过；缺文件、篡改、软链、Unix socket特殊文件、同名冲突、配置注入6项拒绝；每次拒绝后旧活动配置仍200且同SHA，没有激活错误配置 |
| 生命周期 | master15，worker16→18→20，两次HUP；error.log记录旧worker正常退出及最终TERM后master/worker退出；listener与包装进程消失 |
| 宿主/封存 | 前后宿主namespace及常驻Go/Web和宿主Nginx身份快照完全相同；候选完整字节未变；staging已卸载，仅剩空目录/Node零字节占位与安全证据 |

括号策略明确为接受清单中的原始字面`[id]`路径（全量请求包含真实此类文件），拒绝`%5B/%5D`及双编码别名；这是一致的规范URL策略，不声称支持所有客户端路径重编码。`Cache-Control`的300秒是本次HTTP缓存策略，不是允许300秒后删除资源；正式静态保留窗口及清理仍需后续制定。

原生access.log共2152条：200=2119、304=1、400=4、404=24、405=4。包含ready探测、重复字节核验和预期反例，不能把全部非200当作意外失败，也不能把该有界请求观察称作容量或零丢请求的普遍证明。

## 测试与证据

15项Node测试全部PASS：路径/注入/同名冲突、双来源、真实缺失/同大小篡改/root与各级软链/特殊文件、历史静态源外锚与dirty源码拒绝。日志 `/tmp/zjmfgo-static-retention-unit-20261017.log` SHA256 `83b645ad5d0df73e84c9b9ddaa74477b71d68859b93c043c72f6a6e0170ac622`。

本批只增加运行准备脚本、测试及文档，Go/迁移/前端业务源码未变，未重复前一批已通过的强制PG18全量、vet/typecheck/lint及真实webpack构建；相关证据保持在前一批记录。静态HTTP核验也不替代四尺寸业务浏览器验收，105页仍45完成/60部分。

- 原始完整report：`/tmp/zjmfgo-static-retention-run5-20261017/report.json`，825626字节，SHA256 `0841efdac8c5b80b35b6c8a647a5d29f10ef34f3c26f3f727cb01c013e07fb55`。
- [Git摘要投影](evidence/release-static-retention-20261017/run5-summary.json)明确标为projection，保留原report外锚，省略mapping对象与checked/continuous请求数组，保留各自规范JSON摘要及条目数量；完整映射、请求数组、配置和原生日志仍在原运行根中。mapping摘要 `03cd639ac35e6ce379a05f8b7745fa251a1fe2f609f06770513f39a791924021`。
- 执行日志 `/tmp/zjmfgo-static-retention-execute5-20261017.log` SHA256 `f25d808ad1f37626cb321ff458eb75579c66b277f9390de1111f0ff0a5747241`。
- 输出预算32MiB/4096文件，磁盘最低保留512MiB；250ms检查目录用量/空间/inode。[外部资源样点](evidence/release-static-retention-20261017/run5-resource-observations.json)36个、从启动后开始，最低可用1208086528字节/4798188 inode，不覆盖未采样瞬间，不等于峰值保证。
- 主代理核验退出码和报告，`gpt-6.1-sol`独立重算440个来源文件、映射/双来源、三个配置及984条请求摘要，与外锚逐项一致；复核真实原生日志、35份库映射、namespace退出和staging卸载，无遗留该netns进程。

本机`/tmp`完整证据未设置自动删除，但可能因系统重启/清理丢失；Git仅保留小报告投影、摘要和失败记录，不提交依赖、候选二进制或静态文件副本。当前证据未构成永久离机审计归档。

## 保留的失败尝试

[失败记录与摘要](evidence/release-static-retention-20261017/attempts.json)保持原结果，不倒改为PASS：

1. run1预检：sourceProof缺revision，未创建运行根或启动Nginx。
2. run2预检：当前完整runtime verifier正确拒绝candidate2缺SSR API origin。改为历史静态来源专用认证，未修改candidate2或放松新包验证。
3. run3隔离启动：目标Nginx缺少已安装在宿主的jemalloc只读映射，首次`-t`失败；补一个已核验库，未扩大映射整个`/usr/local/lib`。
4. run4真实HTTP检查通过，但最终工具取证将`/dev/zero (deleted)`匿名映射按文件读取失败；保留整体FAIL，精确区分设备映射后完整重跑run5。

## 尚未关闭

本轮是自有静态配置激活/恢复准备，未发送真实新旧Web页面、未切换upstream、未验证SSR/会话/公开Origin或HTTPS。完整双Web需要两个启动声明完整且合法端口/Origin配置的完整候选，实际旧页面引用及新页面引用、真实upstream预热/排空/切换切回；不能给candidate2临时补env来充当旧包通过证据。

G04继续缺实际目标HTTPS/维护与回调、正式资源保留窗口/清理、授权业务快照升级恢复和生产切流切回；G09继续缺目标容量、积压排空、执行中强杀恢复等。未执行资金、供应商或真实通知动作，未改业务数据库或常驻服务。
