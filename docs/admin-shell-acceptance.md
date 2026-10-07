# 管理端主题与公共外壳验收

日期：2026-10-07。范围：`AdminShell`、导航和响应式布局、管理端主题、管理登录、Portal 主题继承，以及客户详情公共身份区和列表 URL 状态；依据 `docs/admin-frontend-design.md`。

## 变更范围

管理端借鉴 Arco Pro 的蓝色主色、中性灰阶、紧凑控件和导航层级，沿用 Next.js、React、shadcn/ui、Radix 和 lucide-react，没有引入 Arco 依赖。客户中心保留绿色主题。管理员退出返回 `/admin-login`。

本批补充了受 `customers.view` 和客户范围保护的 `GET /api/v1/admin/customers/{id}` 客户摘要接口，以及受 `invoices.view` 和客户范围保护的支付/余额流水接口；客户详情页接入真实摘要、联系人、服务、订单、账单、交易、工单和客户文件入口。服务、订单、账单、工单列表增加管理端 `customer_id` 筛选，列表的 `q/page/department` 状态写入 URL，详情入口和返回按钮保留列表查询参数。未修改数据库、验证码配置或业务数据。

## 检查结果

| 检查 | 结果 | 证据与范围 |
| --- | --- | --- |
| TypeScript | PASS | `web` 下 `npm run typecheck` |
| ESLint | PASS | `npm run lint`，0 错误；登录和重置密码组件有两处已有的 `img` 警告 |
| 生产构建 | PASS | 隔离构建目录执行 `API_INTERNAL_URL=http://127.0.0.1:8088 npm run build -- --webpack`，成功后部署至实际 `web/.next` |
| 核心页面响应式 | PASS | 工作台、客户、产品、供应商、订单、服务、账单、工单、设置共 9 页，视口宽度 1440、1024、768、767、390、320px；整页无横向溢出，无 JavaScript 页面异常 |
| 外壳尺寸 | PASS | 顶栏 56px；桌面导航 224px，折叠及平板为 64px；手机隐藏侧栏并使用抽屉 |
| 导航和面包屑 | PASS | 分组可收起/展开；客户详情 `/admin/customers/2` 高亮客户管理，面包屑包含客户管理和联系人页标题 |
| 手机抽屉 | PASS | 焦点约束、Tab、Esc 关闭并返回触发按钮；跨越 768px 断点关闭抽屉并解除背景锁定 |
| 弹窗与选择器主题 | PASS | 实际编辑客户弹窗及选择器 Portal 继承 `#165dff`；桌面主按钮为蓝色 |
| 手机点击尺寸 | PASS | 最终构建在 390px/320px 下，编辑客户弹窗输入框、选择器、保存按钮和关闭入口均为 44px；导航关闭按钮为 44×44px；弹窗左右各保留 16px |
| 客户中心主题隔离 | PASS | 管理端跳至客户中心以及退出后的公共页面均恢复 `--primary: #15705e` |
| 登录与退出 | PASS | 实际 root 登录成功；最终构建退出返回管理登录，随后 `/api/v1/me` 为 401 |
| 前端只读权限呈现 | PASS（模拟） | 浏览器临时将 `/me` 响应设为 `role: admin` 和 `customers.view`，仅显示客户导航，隐藏编辑按钮；随后恢复真实响应 |
| 客户详情摘要与 URL 状态 | PASS | 实际 root 会话访问 `?q=acceptance`，详情 URL 为 `/admin/customers/2?q=acceptance`，返回后搜索框恢复 `acceptance`；摘要接口返回 200、客户 ID 2 |
| 客户范围详情接口 | PASS | `GET /api/v1/admin/customers/2` 返回摘要；接口 SQL 使用 `admin_customer_allowed`，范围外对象统一 404 |
| 客户详情关联分区 | PASS | 实际 root 会话打开服务、订单、账单、工单分区；对应 `customer_id=2` 接口均返回 200，分别返回 2、2、2、1 条，四个分区均正确高亮并渲染表格 |
| 客户交易时间线 | PASS | 实际 root 会话访问交易接口返回 200 和 1 条流水；交易分区高亮并渲染时间线表格；退出后 `/me` 为 401 |
| 后端 RBAC 和业务写入回归 | 部分验证 | 客户摘要及四个关联列表使用真实 root 会话和客户范围 SQL；未验证真实只读管理员的 API 拒绝写入边界；未提交任何业务表单 |
| 全部业务页面状态与交互 | 未验证 | 本批不是逐页业务验收；错误、空态、超时、冲突、重复提交和完整客户标签页需要后续实施与验收 |

部署后 `zjmfgo-api`、`zjmfgo-worker`、`zjmfgo-web` 均为 active，`http://localhost:3088/admin-login` 返回 200。浏览器控制台中的部署窗口连接拒绝及退出后的预期 401 已核对；最终检查没有 JavaScript 页面异常。

## 本机证据

以下证据保存在 Git 忽略的输出目录，仅用于本机复核；临时构建已清理，截图和检查脚本保留，不包含会话凭据：

- `output/playwright/admin-shell-final.png`：最终生产构建桌面工作台。
- `output/playwright/admin-shell-customers.png`：客户列表。
- `output/playwright/admin-shell-tablet.png`：平板图标导航。
- `output/playwright/admin-shell-mobile.png`、`admin-shell-mobile-menu.png`：手机页面及菜单。
- `output/admin-shell-20261007/layout-check.js`：9 页 × 6 个宽度的尺寸检查。
- `output/admin-shell-20261007/navigation-check.js`：主题隔离、分组和模拟只读导航检查。
- `output/admin-shell-20261007/final-check.js`：最终构建手机 Portal、退出和主题隔离检查。

浏览器为 Playwright CLI 独立 Linux Chrome 会话，不是用户当前的内置浏览器会话。测试会话已退出并关闭。
