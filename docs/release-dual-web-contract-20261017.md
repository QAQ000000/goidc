# 隔离双 Web 切换最小可执行契约（2026-10-17）

**状态：计划，未执行、未验收，不计通过。** 文档基线 `7c62a309d5662c05d7da2b7224c8e9818e9c9f44`。本轮文档工作不启动服务、构建或数据库；下文是后续明确执行批次的最小方案。目标是在私有环境用两个合法完整候选、两套 API/Web 和真实目标 Nginx，验证预热、同一公共入口切换/切回、真实页面及资源、同源 API 与 SSR 会话连续性。没有生产切流、业务库或外部业务写入授权。

已有证据可复用：[candidate3 完整 runtime](release-runtime-real-build-acceptance-20261017.md)、[真实 Nginx 静态220→382→382](release-static-retention-acceptance-20261017.md)及[静态保留契约](release-static-retention-contract-20261017.md)。本批补动态 upstream 和公共 Origin 边界，不重复已有 worker 业务演练或无差异全量业务测试；上层[G04/G09契约](release-readiness-next-contract-20261017.md)继续开放。

## 1. 最小拓扑与两个正常构建

全部端口仅存在于本批私有 net namespace；执行前检查实际占用，端口如需更改，先固定新计划并按计划正常构建。下列地址不是宿主服务目标。

| 用途 | 旧代 A | 新代 B |
| --- | --- | --- |
| API 内部地址 | `http://127.0.0.1:58088` | `http://127.0.0.1:58089` |
| Web 内部监听 | `http://127.0.0.1:53088` | `http://127.0.0.1:53089` |
| 两包统一公共 Origin | `http://127.0.0.1:53188` | 同左 |
| Nginx 对外入口 | `127.0.0.1:53188` | 保持同一监听 |
| PG18 | 独占 `_test` 数据库中的同一随机自有 schema；例如专属 PG18 Unix socket经私有桥接到 `127.0.0.1:55439` | 同左 |

最小做法是待构建器支持显式 `publicOrigin` 后，冻结一个最终干净提交，按上表**顺序正常构建两次**。构建请求分别固定 `apiInternalURL`、`webOrigin`，共享 `publicOrigin`；Go/Node/npm、锁文件、只读依赖、构建器文件和预算均有独立锚点。复用现有可信依赖输入，不再 npm ci，不创建新框架、共享依赖目录或大规模构建矩阵。两包各包含原要求的 API、worker、migrate、standalone Web及完整迁移清单，本批仅运行 API/Web和受控迁移。

相同源码的两个真实构建足以验证本批部署代际和端口切换，**不能证明不同历史代码版本的升级兼容性**。如本批确实选两个不同提交，必须都具备新的 Origin 声明能力，并先核对迁移完全一致；出现迁移差异则另立升级兼容性契约，不在本轮临时扩成数据升级演练。最终实际提交、BUILD_ID、产物和manifest SHA均写入执行计划，不用本文件占位代替锚点。

candidate2 缺少 SSR API origin，candidate3 的 `PUBLIC_ORIGIN` 绑定内部 Web53088；它们都不能通过改环境适配第三端口反代。**最终两包必须由构建器正常生成并封存，不能修改旧封存env、launch、server.js、rewrite或manifest后重算摘要来充数。** 旧包继续保留原证据，不原地修补；默认 `publicOrigin=webOrigin` 的兼容行为也不能解释成任意反代 Origin 已受支持。

预检必须验证两包声明及实际字节一致：`HOSTNAME/PORT` 对应各自 Web内部监听，`HTTP_ADDR` 对应各自API，`API_INTERNAL_URL` 同时绑定 SSR 和构建时 rewrite；`PUBLIC_ORIGIN/PUBLIC_ORIGINS` 均为53188公共Origin。公共端口与两API/两Web分开。当前完整 verifier必须接受两包，实际运行环境严格来自合法声明和外部受控配置，不由控制器临时覆盖固定项。该隔离 HTTP 场景使用 `COOKIE_SECURE=false`；不把它作为公网 HTTPS 合格证据。

## 2. 数据、会话及外部边界

使用真实隔离 PG18，核对版本、数据库名、专属 socket、随机 schema及每个连接的 search_path。两API共用该schema与同一组仅用于夹具的加密配置，确保同一会话真实跨实例可用；不以两个独立库复制cookie来伪造连续性。先用A migrate建表并重复验证，再以B migrate校验同一完整迁移集；不自动down、不连接业务库或复用业务schema。

只准备最小客户用户、站点设置和真实登录会话，账号密码随机，仅保存在自有0600控制文件；关闭该schema内验证码要求和外部通知等会干扰夹具的配置，并核对实际值。无供应商账号/凭据，无支付、退款、邮件、短信或回调目标，无待执行业务任务，不启动worker；注册、支付、订单和供应商操作均不属于本批。允许写入仅限夹具准备、真实登录/注销及相关会话/审计元数据；其他表按前后摘要核对，不能用总行数代替不变量。

应用所在namespace没有外部网络，最多只映射这个明确PG18 socket；没有业务配置、宿主业务库socket或真实密钥。配置不继承宿主环境，不输出DSN、密码、Cookie值、会话token或完整请求/响应头。SSR与API使用真实Go实现，不以Node模拟后端。

## 3. Nginx、静态并集与配置接管

复用已经验收的目标 `/www/server/nginx/sbin/nginx` 和必要系统/Lua库方案，执行时重核摘要及实际加载库；Node只作控制器和客户端。私有 mount/net空间、真实普通用户UID/GID、能力与附加组清空、自有独占运行根、只读发布物和声明可写cache/tmp/log等边界保持有效。没有常驻站点目录或宿主vhost include，禁止宿主reload。

Nginx只使用自有 `-p/-c/-e` 和明确前台运行参数；master/worker及双API/Web的实际exe、cwd、参数、启动tick、UID/GID、namespace、监听和源码产物摘要都要记录。只在核验master身份后向该PID发HUP；先真实 `-t` 再原子替换配置。实际generation响应、新worker及upstream行为证明接管，命令成功和PID文件不足以证明切换。

- `/_next/static/` 使用两最终包实际清单生成的精确并集。数量重新计算，不硬编码历史382；同URL不同字节、文件缺失、篡改、链接或路径注入阻断激活。并集在新页面首次发出前装好，此后A→B→A始终保留全部A/B资源；不删除B独有资源。
- `/api/` 直接到活动GoAPI，页面到同代Web；每份配置成对选择A/A或B/B，不能为省事一直固定旧API。HUP交接期允许仍在排空的旧worker继续使用其完整A/A配置，接管后用新连接核验B/B；恢复反向相同。停止旧进程必须晚于其worker排空。
- 公共Host必须保留端口，如固定 `127.0.0.1:53188` 或经allowlist校验的 `$http_host`，不能用丢失端口的 `$host` 冒充同源。明确覆盖 `X-Forwarded-Host/Proto`、来源地址头并清除不可信 `Forwarded`；Origin透传并由Go核验。只接收计划内Host，不能依客户端伪造转发头生成公开链接或放宽来源。
- HTML、API、受保护页及错误响应 `Cache-Control: no-store`，禁用proxy cache；静态沿用明确的 `public, max-age=300, immutable` 和真实字节/MIME/HEAD校验，错误 `no-store`。300秒不是删除窗口；本批全程不清理并集，正式保留周期/清理需后续固定。
- 静态原始URI、未知路径、`.env`、方法、编码/双编码、MIME、nosniff及条件请求拒绝规则继续成立；未知静态不得落入Next页面fallback。这里只复核合并动态代理后可能受影响的边界，复用已有无差异纯函数/文件负例证据。

## 4. 最小真实验收步骤

执行前固定全部输入和预算，控制器默认预检，显式执行才创建运行根并启动子进程。所有HTTP请求由私有namespace内发出，不触及宿主3088/8088或公网。每步有deadline，失败停止切换并进入自有清理。

| 步骤 | 可执行动作 | 放行证据 |
| --- | --- | --- |
| A.来源与建库 | 复核双包、runner、Nginx和预算锚点；建立专属PG18/schema，完成同迁移集校验和安全夹具 | 来源干净、真实字节一致、无外部渠道/任务；两API可用同一schema |
| B.两代预热 | 启动A/B API，分别 `/readyz`；再启动各自Web；经各自Web请求站点/API rewrite和真实登录页 | 每实例有界ready；内部 `/api/v1/site` 与该代API相符；SSR站点内容含夹具标记，不是getSite失败fallback；各自实际BUILD_ID和运行文件相符 |
| C.公共入口A | 启动Nginx，静态并集已就位，页面/API选A；获取 `/`、`/login`、`/admin-login` 和匿名 `/console/contracts` | 实际HTML资源列表全部可从公共Origin取到且SHA符合A清单，至少一个A独有引用；匿名受保护页真实跳登录；公共链接/redirect无内部53088/53089地址 |
| D.建立会话 | 经53188 `POST /api/v1/auth/login`，带正确Origin及 `X-Requested-With: zjmfgo`；客户cookie请求 `/api/v1/me` 和 `/console/contracts` | 登录返回真实session cookie，HttpOnly、Path=/、SameSite等符合固定策略；API身份与夹具相符，SSR受保护页200且通过真实requireUser，无登录跳转 |
| E.切到B | 连续请求A旧chunk、公共HTML和只读 `/api/v1/me`；检查B ready后激活B/B配置并HUP | 新worker/generation及后端连接目标证明B/B接管；新HTML资源符合B且至少一个B独有引用；同一个cookie在API/SSR仍为同一客户；A/B资源全部同封存字节 |
| F.排空与切回 | 等待A配置worker正常排空，再准备A ready；激活A/A配置并HUP，验证B配置worker排空 | 真实A页面重新出现并符合A清单、同cookie API/SSR连续；B独有chunk继续200同字节；切换/恢复耗时和退出worker身份可核对 |
| G.来源与注销 | 使用无cookie、随机无效cookie、错误Origin、伪造Host/Forwarded等反例；最后经 `POST /api/v1/auth/logout` 正确来源注销 | 无效会话API401、SSR跳登录；错误Origin写请求403且原会话仍可用；伪造来源不能绕过；正确注销后A/B API及两Web SSR都拒绝旧cookie |
| H.退出 | 停止自有Nginx与双Web/API，移除自有schema/PG及映射；复核候选和宿主身份 | 自有进程组/listener退出、schema移除且专属PG按所有权清理、封存不变、常驻PID/启动tick/exe及宿主namespace不变；保留脱敏证据 |

会话与SSR检查应覆盖A、B内部及公共入口，不只比较HTML200。用实际 `requireUser` 的 `/console/contracts` 作为最小SSR路径；以匿名/有效/注销后的不同结果及实际连到该代API `/api/v1/me` 的脱敏证据共同证明SSR路由。Nginx原生日志至少记录generation、请求路径、状态、upstream地址；对Web→API的SSR连接保留syscall地址元数据或受控API日志，不记录Cookie/写入缓冲内容。纯generation header不能单独证明后端身份。

HTML可能含动态值，不要求前后整页SHA相等；记录每份实际HTML摘要、资源引用清单和每项来源摘要，并断言至少一个代际独有引用。如果真实两构建未出现任何独有页面资源，本轮“独有资源”格未通过；不得改chunk名、注入脚本、篡改封存字节或拿未被页面引用的文件替代。先检查真实构建差异，再决定是否需要另一个正常源码候选，不盲目反复构建。

持续请求建议每次响应后10ms继续，覆盖切换前、HUP期间、稳定后及切回；记录请求开始/结束、generation、状态、字节SHA和错误数。新连接证明新worker接管，另有有界长连接观察旧worker排空；不依赖sleep猜排空。keepalive/worker_shutdown_timeout须在配置中明确，建议客户端keepalive上限5秒、worker排空上限15秒，超过上限记FAIL。不要为演练慢请求新增业务接口；可用现有足够大静态文件的受控读取观察。完整会话只读检查串行，避免注销与持续探测竞态被错算成切换错误。

切换/切回期间不停止待回退API/Web，不启动新worker；排空是Nginx旧配置worker及在途HTTP请求，不扩称worker任务排空。此方案模拟真实进程/网络/数据库边界，但仍是有界请求验收，不能推导目标容量或所有浏览器标签页长期兼容。

## 5. 预算、失败处理与证据边界

两次正常构建分开顺序执行、各用现有预算预检和监控，不并行争用构建临时空间。双包封存后才运行动态验收；资源计划必须同时计入双封存、实际run副本或只读staging、双API/Web RSS、Nginx、PG18、cache、日志和证据。可复用只读输入以减少副本，但不能用硬链接/改权限造成可写共享。执行前分别核对根盘/tmpfs/PG所在文件系统，至少保留512MiB和10000 inode；不能把历史单包3GiB预算直接当成双包批准。

验收本身建议15分钟硬上限、每实例ready30秒、TERM15秒、HUP后接管及旧worker排空各15秒；配置和执行计划可以在运行前调整，实际值固定进报告。250ms检查自有目录/日志用量与可用空间，外部记录内存及空间采样；采样只能报告观察峰值，不能承诺未采样瞬时峰值。日志有明确大小/条数上限，触限或保留量不足立即停止自有任务，不能清理其他任务文件来继续。

失败时不自动重建、不改封存配置、不触业务服务；若已经切到B但A仍健康，可只对自有实例执行预定A/A恢复，保留静态并集并记录失败，不把恢复成功改写成整体PASS。任何强制KILL、schema清理未证明、包摘要变化、外部网络/写入尝试、未知文件可读、Origin绕过、会话丢失、页面/资源混版或chunk404都阻断通过。清理不能按模糊目录/PID模式删除或杀进程。

报告至少绑定最终两提交/BUILD_ID/manifest外锚、构建请求及锁定工具链、runner提交、全部配置和映射摘要、运行进程/库/namespace身份、迁移/schema证明、真实HTML及引用资源核验、API/SSR会话结果、Origin反例、逐阶段切换/排空/恢复时间、持续请求原始数组、资源观察和退出不变证明。敏感夹具配置和cookie清理，小报告保留摘要和精确外部证据位置；完整证据需说明保留期限，不能把易失/tmp当永久离机归档。

此批通过最多关闭“隔离完整双Web、统一公共Origin及upstream切换/切回准备”子项。仍不证明真实历史代码/schema升级、业务快照恢复、真实支付/供应商/通知、公网HTTPS/证书/安全Cookie、真实域名与端口暴露、生产维护和回调、正式资源清理窗口、目标容量、业务worker崩溃/任务排空或生产切流。公网批次必须使用相应真实目标正常构建的合法候选，不能临时改本批loopback封存env晋升；G04/G09仅在各自剩余范围实测后更新。
