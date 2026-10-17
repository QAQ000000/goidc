# 客户字段贯穿注册最小实施契约

状态：本批已实现并完成隔离PG18、相关race和四尺寸浏览器验收，见[执行记录](customer-registration-fields-acceptance-20261016.md)。原始规划基线主分支defa346；以下保留设计契约，不将要求本身当作运行证明。范围为#28客户自定义字段、#46注册登录设置、#98新增客户及既有客户详情、公开注册页之间的字段定义/值一致性，不扩展联系人登录、手机注册、OAuth或第三方实名。不改现有登录凭据与RBAC语义。

## 既有能力与真实缺口

`internal/httpapi/customer_custom_fields.go`已有客户字段CRUD：field_key、label、text/textarea/number/date/select/boolean、description、validation_pattern、choices、sort_order、admin_only、required、show_on_register、active，以及版本、幂等、权限和审计。`internal/httpapi/server.go`已注册后台customer-fields与customer-field-definitions端点。

`internal/httpapi/customer_profile_contract.go`的后台定义接口读取所有active字段（包括内部字段），提供definitions_version；`customer_profile.go`已有validateCustomerCustomFields与customer_profiles.custom_fields写值。该验证器读取全部active字段，不能直接拿来给匿名注册强制填写内部required字段。后台创建/详情的类型化填写已有实现，不重复重建。

`web/src/components/customer-field-inputs.tsx`已有六种输入、完整分页加载时definitions_version一致性检查、类型化值与失败重试；当前hook固定访问后台定义API，不能在注册页直接复用其数据来源。`admin-customer-custom-fields.tsx`已有配置页。`web/src/app/register/page.tsx`复用AuthForm；`auth-form.tsx`注册目前只提交固定身份/验证码字段，不加载客户字段。

`internal/httpapi/auth.go` register在外部预检后，又在事务内FOR SHARE复验site_settings.registration_enabled与auth_settings.registration_email_enabled，事务写users、carts、邮箱验证结果、归因及审计；没有自定义字段输入或profile写入。`auth_settings.go`与GET/PUT /admin/auth-settings已有注册/认证配置，公开站点设置供AuthForm读取；本批复用已有注册开关，不另建“假生效”开关。

## 最小方向与公开schema

新增匿名只读GET /api/v1/auth/registration-fields（拟定名），只返回active AND show_on_register AND NOT admin_only的定义，按sort_order/id稳定排序；返回items及opaque definitions_version。最小可采用有明确数量/响应上限的一次完整列表，若分页则每页必须版本一致且客户端验证完整性。注册关闭时不得意外绕过既有策略；接口按现站点公开策略返回禁用状态或403，前端一致处理。

公开字段采用显式投影白名单：field_key、label、field_type、description、required、choices、必要的校验说明。不得复用后台SELECT或返回admin_only/内部标记、管理ID/作者、审计、历史值、客户数据、内部字段名称或正则。validation_pattern默认仅服务端执行；若产品确需公开pattern须另作明确决定，不能因组件类型已有该属性便全部暴露。描述文本作为纯文本渲染。opaque版本不能编码内部字段内容。

后台允许既有admin_only/show_on_register组合历史数据存在，但公开选择必须以NOT admin_only为硬边界；新增/编辑配置可明确提示两者冲突或禁止同时开启。内部required不能成为公开注册必填要求。公开schema不是授权依据：提交仍按当前可公开集合校验。

## 注册事务与最小API改动

POST /auth/register新增custom_fields（按field_key的类型化对象）及custom_fields_version。事务中获取与字段CRUD协调的定义锁/版本快照，复验注册开关和当前公开集合，检查定义版本，服务端执行必填、长度、数字、真实日期、布尔、选项、正则等规则。复用现类型校验逻辑的纯函数部分或显式scope参数，保持后台验证全部active字段的既有行为；不能全局删掉admin_only校验。

拒绝提交未知、停用、不公开或内部field_key，不能悄悄接受也不能写入其他profile固定字段。只保存该次已验证公开值；字段省略/空值规则与后台类型约定一致，false/0不得当空。profile与users/carts/归因/审计在同一事务提交；任何字段错误或审计失败不得留下用户、购物车或profile半记录。不得把值写入普通审计payload或日志。现验证码消耗有独立语义，不能未经核对承诺所有验证码都会随注册失败回滚。

字段定义在表单打开后改变，返回409 registration_fields_changed（拟定code），不创建用户；前端保留安全草稿并明确加载最新schema，删除已不允许的值后由用户重审再提交，不能自动重发。没有公开字段时旧注册请求保持兼容；存在公开字段时漏版本不得绕过必填与当前定义校验，明确返回冲突/校验错误。失败码沿现注册400/403/409策略区分输入、注册关闭、定义变化和重复身份。

后台字段CRUD继续现expected_version、Idempotency-Key及权限/审计；不借本批重构已实现后台客户创建/详情幂等。公开GET无写幂等要求。注册本批不顺带新增匿名通用幂等账本：现注册身份唯一约束不等于同键响应重放；未知结果提示用户检查注册结果/尝试登录或找回，不自动再建。若决定新增注册幂等，需另定与邮箱/验证码绑定及密码存储边界，禁止持久化明文密码/原注册body。

## 前端最小变更

AuthForm仅register分支加载公开schema，loading/error时不能跳过required继续提交；无字段正常展示，失败可重试。复用CustomerFieldInputs的纯渲染和customerFieldValues，拆开数据hook使后台地址保持不变，新增公开注册数据源。不要为了复用把后台API开放匿名。

字段管理页show_on_register/admin_only语义与公开注册对应，提供真实预览说明，不造假字段；认证设置页沿用已有开关及取消/冲突保护，不增加无实际后端能力控件。注册校验失败保留非敏感值；密码/验证码只留内存，不写local/sessionStorage、URL、截图或去敏记录。schema冲突加载最新须明确比较与替换，避免暗中丢值；注册成功保持现跳转语义。

## 验收要求（实际结果见执行记录）

隔离PG18 `_test`随机schema：六字段类型、required/optional、空/null/false/0、真实日期、非法选项/regex/超长；内部required、admin_only+show_on_register、停用/隐藏/未知key绝不暴露或可写；无字段旧请求兼容。匿名与后台schema各自范围正确，分页版本漂移/字段定义并发更改返回409，关闭注册时schema/提交遵循同一策略。审计故障、profile写失败及并发重复邮箱不留下半记录；已有后台字段/创建详情/认证注册/验证码/归因相关回归保持。

四宽1440/1024/390/320：字段配置→注册加载→填写→提交→后台详情核对真实值；loading/error/empty、六输入键盘可达、长描述/移动端滚动、取消/字段变更409/明确重新加载、注册关闭与失败恢复。用明确隔离fixture真实定义/值，无硬编码假数据；不发真实邮件短信，验证码测试走现隔离方式。记录console/pageerror及预期403/409，截图去除密码与验证码。

完成批次需scripts/test-all.sh、相关race、Go build/vet、前端typecheck/lint及隔离build，实际执行范围与证据见本批验收。最初只读契约阶段未运行fixture或验收，不能追认为当时已实现。生产邮件/短信及公开上线验证另列。
