package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type Permission struct {
	Code  string `json:"code"`
	Group string `json:"group"`
	Label string `json:"label"`
}

// These are new API capabilities, not aliases for PHP auth_rule IDs.
var AdminPermissions = []Permission{
	{"settings.view", "站点设置", "读取站点设置"}, {"settings.manage", "站点设置", "修改站点设置"},
	{"dashboard.view", "工作台", "读取运营汇总"},
	{"customers.view", "客户", "读取客户和联系人"}, {"customers.manage", "客户", "修改客户、联系人和密码"},
	{"customer_groups.view", "客户分组", "读取分组"}, {"customer_groups.manage", "客户分组", "管理分组和客户归属"},
	{"customer_levels.view", "客户等级", "读取客户等级规则"}, {"customer_levels.manage", "客户等级", "管理客户等级规则"},
	{"promos.view", "优惠码", "读取优惠码"}, {"promos.manage", "优惠码", "管理优惠码"},
	{"products.view", "产品", "读取商品及目录分组"}, {"products.manage", "产品", "管理商品、价格与目录分组"},
	{"resources.view", "服务器资源", "读取供应商服务器资源列表"}, {"resources.manage", "服务器资源", "管理服务器资源信息"},
	{"content.view", "内容", "读取文章与分类"}, {"content.manage", "内容", "管理新闻、知识库及分类"},
	{"downloads.view", "下载中心", "读取文件、分类和下载设置"}, {"downloads.manage", "下载中心", "上传、编辑、删除文件及管理分类与设置"},
	{"contracts.view", "合同管理", "读取授权客户合同"}, {"contracts.manage", "合同管理", "生成审核寄送与作废合同"},
	{"customer_files.view", "客户文件", "读取授权客户文件与下载"}, {"customer_files.manage", "客户文件", "上传、编辑、转移和删除授权客户文件"},
	{"orders.view", "订单", "读取订单"}, {"orders.manage", "订单", "审核、备注和取消订单"},
	{"invoices.view", "财务", "读取账单"}, {"invoices.manage", "财务", "创建、编辑和作废人工账单"}, {"invoices.refund", "财务", "退款"}, {"balance.manage", "财务", "客户余额充值"},
	{"credit.view", "信用额", "读取客户信用额"}, {"credit.manage", "信用额", "调整客户信用额"},
	{"payments.reconcile", "财务", "查询在线支付并补结算"},
	{"invoice_applications.view", "发票", "读取授权客户的发票申请"}, {"invoice_applications.manage", "发票", "审核、登记开具及邮寄信息"},
	{"services.view", "服务", "读取服务和取消申请"}, {"services.manage", "服务", "执行服务动作、交付和取消审核"},
	{"tickets.view", "工单", "读取授权部门工单、附件和内部备注"}, {"tickets.manage", "工单", "回复、分派、转移、合并和管理附件"},
	{"ticket_presets.view", "预设回复", "读取预设回复"}, {"ticket_presets.manage", "预设回复", "管理预设回复及分类"},
	{"notifications.view", "通知", "读取模板和邮件队列"}, {"notifications.manage", "通知", "修改模板和重试邮件"},
	{"affiliates.view", "推广返利", "读取推广账户和归因"}, {"affiliates.manage", "推广返利", "审核推广账户"},
	{"affiliates.withdrawals.review", "推广返利", "审核提现申请"}, {"affiliates.payout", "推广返利", "登记线下打款及读取收款账户"},
	{"certifications.view", "实名认证", "读取实名认证申请和资料"}, {"certifications.manage", "实名认证", "审核实名认证申请"},
	{"sales.view", "销售", "读取销售设置、提成组和业绩范围"}, {"sales.manage", "销售", "管理销售分配、提成组、阶梯和人员绑定"},
	{"security.view", "安全", "读取登录黑名单和安全事件"}, {"security.manage", "安全", "管理登录黑名单和解除登录阻断"},
	{"reports.view", "报表", "读取经营报表"}, {"audit.view", "审计", "读取操作日志"}, {"migrations.view", "迁移", "读取迁移记录和复核详情"},
	{"jobs.view", "任务", "读取开通和通知任务队列"},
}

func settingsManagementTx(ctx context.Context, s *Store, actorID int64) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'settings.manage'=ANY(r.permissions)),false)
 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed)
	if err != nil || !allowed {
		tx.Rollback(ctx)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, conflict("permission_denied", "没有管理站点设置的权限")
			}
			return nil, err
		}
		return nil, conflict("permission_denied", "没有管理站点设置的权限")
	}
	return tx, nil
}

func recheckSettingsManager(ctx context.Context, tx pgx.Tx, actorID int64, sessionHash string) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", actorID).Scan(&role, &roleID); err != nil {
		if err == pgx.ErrNoRows {
			return conflict("permission_denied", "没有管理站点设置的权限")
		}
		return err
	}
	var sessionUserID int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", sessionHash, actorID).Scan(&sessionUserID); err != nil {
		if err == pgx.ErrNoRows {
			return conflict("unauthorized", "登录已失效")
		}
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return conflict("permission_denied", "没有管理站点设置的权限")
	}
	var allowed bool
	if err := tx.QueryRow(ctx, "SELECT active AND 'settings.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&allowed); err != nil {
		if err == pgx.ErrNoRows {
			return conflict("permission_denied", "没有管理站点设置的权限")
		}
		return err
	}
	if !allowed {
		return conflict("permission_denied", "没有管理站点设置的权限")
	}
	return nil
}

// RecheckSettingsManager is used by settings endpoints that keep a wider
// transaction than the legacy store helpers.
func RecheckSettingsManager(ctx context.Context, tx pgx.Tx, actorID int64, sessionHash string) error {
	return recheckSettingsManager(ctx, tx, actorID, sessionHash)
}

func (s *Store) AdminGrants(ctx context.Context, userID int64) ([]string, error) {
	var grants []string
	err := s.Pool.QueryRow(ctx, `SELECT coalesce((SELECT r.permissions FROM admin_roles r WHERE r.id=u.admin_role_id AND r.active),'{}'::text[])
 FROM users u WHERE u.id=$1 AND u.status='active'`, userID).Scan(&grants)
	return grants, err
}

type AdminRoleInput struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Active          bool     `json:"active"`
	Permissions     []string `json:"permissions"`
	ExpectedVersion int64    `json:"expected_version"`
}

func validateRole(in *AdminRoleInput) error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 80 || utf8.RuneCountInString(in.Description) > 500 || len(in.Permissions) > len(AdminPermissions) {
		return conflict("invalid_role", "角色名称、描述或权限无效")
	}
	valid := map[string]bool{}
	for _, p := range AdminPermissions {
		valid[p.Code] = true
	}
	for _, p := range in.Permissions {
		if !valid[p] {
			return conflict("invalid_permission", "存在未知或不可分配的权限")
		}
	}
	slices.Sort(in.Permissions)
	in.Permissions = slices.Compact(in.Permissions)
	if in.Permissions == nil {
		in.Permissions = []string{}
	}
	// A mutation grant always includes the corresponding read capability.
	for _, p := range in.Permissions {
		view := strings.TrimSuffix(p, ".manage") + ".view"
		if p == "invoices.refund" || p == "payments.reconcile" {
			view = "invoices.view"
		}
		if valid[view] && view != p && !slices.Contains(in.Permissions, view) {
			return conflict("missing_read_permission", "操作权限必须同时勾选对应读取权限")
		}
	}
	return nil
}

func adminManagementTx(ctx context.Context, s *Store, actorID int64) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108190)"); err == nil {
		var id int64
		err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND role='super_admin' AND status='active' FOR SHARE", actorID).Scan(&id)
	}
	if err != nil {
		tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conflict("permission_denied", "仅超级管理员可管理人员权限")
		}
		return nil, err
	}
	return tx, nil
}

func (s *Store) SaveAdminRole(ctx context.Context, actorID, id int64, in AdminRoleInput) (int64, error) {
	if err := validateRole(&in); err != nil {
		return 0, err
	}
	tx, err := adminManagementTx(ctx, s, actorID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	action := "admin_role.created"
	if id == 0 {
		err = tx.QueryRow(ctx, "INSERT INTO admin_roles(name,description,active,permissions) VALUES($1,$2,$3,$4) RETURNING id", in.Name, in.Description, in.Active, in.Permissions).Scan(&id)
	} else {
		action = "admin_role.updated"
		var version int64
		if err = tx.QueryRow(ctx, "SELECT version FROM admin_roles WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
			return 0, err
		}
		if version != in.ExpectedVersion {
			return 0, conflict("version_conflict", "角色已变更，请重新加载")
		}
		_, err = tx.Exec(ctx, "UPDATE admin_roles SET name=$2,description=$3,active=$4,permissions=$5,version=version+1,updated_at=now() WHERE id=$1", id, in.Name, in.Description, in.Active, in.Permissions)
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actorID, action, "admin_role", id, map[string]any{"name": in.Name, "active": in.Active, "permissions": in.Permissions}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DeleteAdminRole(ctx context.Context, actorID, id, version int64) error {
	tx, err := adminManagementTx(ctx, s, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	if err = tx.QueryRow(ctx, "SELECT version FROM admin_roles WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		return err
	}
	if current != version {
		return conflict("version_conflict", "角色已变更，请重新加载")
	}
	var assigned bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE admin_role_id=$1)", id).Scan(&assigned); err != nil {
		return err
	}
	if assigned {
		return conflict("role_in_use", "角色仍有关联管理员，无法删除")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM admin_roles WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "admin_role.deleted", "admin_role", id, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type StaffInput struct {
	Username            string   `json:"username"`
	Email               string   `json:"email"`
	Name                string   `json:"display_name"`
	Status              string   `json:"status"`
	RoleID              int64    `json:"admin_role_id"`
	Password            string   `json:"password"`
	ExpectedVersion     int64    `json:"expected_version"`
	TicketScope         *string  `json:"ticket_scope"`
	TicketUnassigned    *bool    `json:"ticket_unassigned"`
	TicketDepartmentIDs *[]int64 `json:"ticket_department_ids"`
	CustomerScope       *string  `json:"customer_scope"`
	CustomerOwnerless   *bool    `json:"customer_ownerless"`
	SalesEnabled        *bool    `json:"sales_enabled"`
}

func (s *Store) SaveStaff(ctx context.Context, actorID, id int64, in StaffInput) (int64, error) {
	return s.saveStaff(ctx, actorID, id, "", "", in)
}

// SaveStaffWithSession rechecks the browser credential after acquiring the
// management lock and records a payload-bound receipt for safe retries.
func (s *Store) SaveStaffWithSession(ctx context.Context, actorID, id int64, sessionHash, key string, in StaffInput) (int64, error) {
	if sessionHash == "" {
		return 0, conflict("unauthorized", "登录已失效")
	}
	return s.saveStaff(ctx, actorID, id, sessionHash, key, in)
}

func (s *Store) saveStaff(ctx context.Context, actorID, id int64, sessionHash, key string, in StaffInput) (int64, error) {
	if key != "" && (len(key) < 8 || len(key) > 128) {
		return 0, conflict("invalid_idempotency_key", "幂等键长度必须为 8 到 128 位")
	}
	in.Username, in.Email, in.Name = strings.TrimSpace(in.Username), strings.TrimSpace(in.Email), strings.TrimSpace(in.Name)
	address, err := mail.ParseAddress(in.Email)
	if err != nil || address.Address != in.Email || len(in.Email) > 254 || in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || in.RoleID < 1 || (in.Status != "active" && in.Status != "disabled") {
		return 0, conflict("invalid_staff", "管理员资料、角色或状态无效")
	}
	if len(in.Username) < 3 || len(in.Username) > 48 {
		return 0, conflict("invalid_staff", "用户名长度必须为 3 到 48 位")
	}
	for _, c := range in.Username {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return 0, conflict("invalid_staff", "用户名只能包含小写字母、数字、横线和下划线")
		}
	}
	var hash []byte
	if id == 0 || in.Password != "" {
		if len(in.Password) < 8 || len(in.Password) > 72 {
			return 0, conflict("invalid_password", "密码长度必须为 8 到 72 字节")
		}
		hash, err = bcrypt.GenerateFromPassword([]byte(in.Password), 12)
		if err != nil {
			return 0, err
		}
	}
	tx, err := adminManagementTx(ctx, s, actorID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if sessionHash != "" {
		var userID int64
		if err = tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", sessionHash, actorID).Scan(&userID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, conflict("unauthorized", "登录已失效")
			}
			return 0, err
		}
	}
	operation := "admin_staff.save." + strconv.FormatInt(id, 10)
	hashInput := in
	hashInput.Password = ""
	raw, err := json.Marshal(hashInput)
	if err != nil {
		return 0, err
	}
	digest := sha256.Sum256(raw)
	requestHash := hex.EncodeToString(digest[:])
	if key != "" {
		var savedHash string
		var result []byte
		err = tx.QueryRow(ctx, "SELECT request_hash,result FROM admin_setting_operations WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3", actorID, operation, key).Scan(&savedHash, &result)
		if err == nil {
			if savedHash != requestHash {
				return 0, conflict("idempotency_conflict", "相同幂等键对应不同的管理员资料")
			}
			var saved struct {
				ID            int64  `json:"id"`
				PasswordProof string `json:"password_proof,omitempty"`
			}
			if err = json.Unmarshal(result, &saved); err != nil {
				return 0, err
			}
			if (in.Password == "") != (saved.PasswordProof == "") || (in.Password != "" && bcrypt.CompareHashAndPassword([]byte(saved.PasswordProof), []byte(in.Password)) != nil) {
				return 0, conflict("idempotency_conflict", "相同幂等键对应不同的管理员密码")
			}
			return saved.ID, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}

	var active bool
	if err = tx.QueryRow(ctx, "SELECT active FROM admin_roles WHERE id=$1 FOR SHARE", in.RoleID).Scan(&active); err != nil {
		return 0, err
	}
	if !active && (id == 0 || in.Status == "active") {
		return 0, conflict("role_disabled", "请选择已启用的角色")
	}
	oldScope, oldUnassigned, oldDepartments := "departments", false, []int64{}
	oldCustomerScope, oldOwnerless, oldSalesEnabled := "owned", false, false
	if id != 0 {
		if err = tx.QueryRow(ctx, `SELECT ticket_scope,ticket_unassigned,ARRAY(SELECT department_id FROM admin_ticket_departments WHERE user_id=$1 ORDER BY department_id) FROM users WHERE id=$1`, id).Scan(&oldScope, &oldUnassigned, &oldDepartments); err != nil {
			return 0, err
		}
		if err = tx.QueryRow(ctx, "SELECT customer_scope,customer_ownerless,sales_enabled FROM users WHERE id=$1", id).Scan(&oldCustomerScope, &oldOwnerless, &oldSalesEnabled); err != nil {
			return 0, err
		}
	}
	customerScope, ownerless, salesEnabled := oldCustomerScope, oldOwnerless, oldSalesEnabled
	if in.CustomerScope != nil {
		customerScope = *in.CustomerScope
	}
	if in.CustomerOwnerless != nil {
		ownerless = *in.CustomerOwnerless
	}
	if in.SalesEnabled != nil {
		salesEnabled = *in.SalesEnabled
	}
	if customerScope != "all" && customerScope != "owned" && customerScope != "assigned" {
		return 0, conflict("invalid_customer_scope", "客户范围无效")
	}
	if customerScope == "all" {
		ownerless = false
	}
	scope, unassigned, departments := oldScope, oldUnassigned, slices.Clone(oldDepartments)
	if in.TicketScope != nil {
		scope = *in.TicketScope
	}
	if in.TicketUnassigned != nil {
		unassigned = *in.TicketUnassigned
	}
	if in.TicketDepartmentIDs != nil {
		departments = slices.Clone(*in.TicketDepartmentIDs)
	}
	if scope != "all" && scope != "departments" {
		return 0, conflict("invalid_ticket_scope", "工单部门范围无效")
	}
	if len(departments) > 1000 {
		return 0, conflict("invalid_ticket_scope", "所选部门过多")
	}
	if scope == "all" {
		departments = []int64{}
		unassigned = false
	} else {
		slices.Sort(departments)
		departments = slices.Compact(departments)
		for _, department := range departments {
			if department < 1 {
				return 0, conflict("invalid_ticket_scope", "工单部门编号无效")
			}
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM ticket_departments WHERE id=ANY($1::bigint[])", departments).Scan(&count); err != nil {
			return 0, err
		}
		if count != len(departments) {
			return 0, conflict("invalid_ticket_scope", "所选工单部门不存在")
		}
	}
	action := "admin_staff.created"
	if id == 0 {
		err = tx.QueryRow(ctx, "INSERT INTO users(username,email,display_name,role,status,admin_role_id,password_hash) VALUES($1,$2,$3,'admin',$4,$5,$6) RETURNING id", in.Username, in.Email, in.Name, in.Status, in.RoleID, string(hash)).Scan(&id)
		if err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO carts(user_id) VALUES($1)", id)
		}
	} else {
		action = "admin_staff.updated"
		var currentVersion int64
		var role, oldStatus, oldEmail, oldUsername string
		var oldRoleID *int64
		if err = tx.QueryRow(ctx, "SELECT role,admin_version,status,email,username,admin_role_id FROM users WHERE id=$1 FOR UPDATE", id).Scan(&role, &currentVersion, &oldStatus, &oldEmail, &oldUsername, &oldRoleID); err != nil {
			return 0, err
		}
		if role != "admin" || id == actorID {
			return 0, conflict("protected_account", "该账号受保护，请使用个人账户设置")
		}
		if currentVersion != in.ExpectedVersion {
			return 0, conflict("version_conflict", "管理员资料已变更，请重新加载")
		}
		_, err = tx.Exec(ctx, "UPDATE users SET username=$2,email=$3,display_name=$4,status=$5,admin_role_id=$6,password_hash=CASE WHEN $7='' THEN password_hash ELSE $7 END,admin_version=admin_version+1,updated_at=now() WHERE id=$1", id, in.Username, in.Email, in.Name, in.Status, in.RoleID, string(hash))
		if err != nil {
			return 0, err
		}
		if hash != nil || oldStatus != in.Status || oldEmail != in.Email || oldUsername != in.Username || oldRoleID == nil || *oldRoleID != in.RoleID || scope != oldScope || unassigned != oldUnassigned || !slices.Equal(departments, oldDepartments) || customerScope != oldCustomerScope || ownerless != oldOwnerless || salesEnabled != oldSalesEnabled {
			if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
				return 0, err
			}
			if _, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", id); err != nil {
				return 0, err
			}
			if _, err = tx.Exec(ctx, "DELETE FROM mfa_challenges WHERE user_id=$1", id); err != nil {
				return 0, err
			}
			if _, err = tx.Exec(ctx, "UPDATE password_reset_tokens SET consumed_at=now() WHERE user_id=$1 AND consumed_at IS NULL", id); err != nil {
				return 0, err
			}
		}
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET ticket_scope=$2,ticket_unassigned=$3,customer_scope=$4,customer_ownerless=$5,sales_enabled=$6 WHERE id=$1", id, scope, unassigned, customerScope, ownerless, salesEnabled); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM admin_ticket_departments WHERE user_id=$1", id); err != nil {
		return 0, err
	}
	for _, department := range departments {
		if _, err = tx.Exec(ctx, "INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", id, department); err != nil {
			return 0, err
		}
	}
	if err = Audit(ctx, tx, actorID, action, "user", id, map[string]any{"username": in.Username, "status": in.Status, "admin_role_id": in.RoleID, "password_changed": hash != nil, "ticket_scope": scope, "ticket_unassigned": unassigned, "ticket_department_ids": departments, "customer_scope": customerScope, "customer_ownerless": ownerless, "sales_enabled": salesEnabled}); err != nil {
		return 0, err
	}
	if key != "" {
		// Keep the expensive password verifier; never persist a fast digest of a
		// password in an idempotency receipt.
		result, _ := json.Marshal(struct {
			ID            int64  `json:"id"`
			PasswordProof string `json:"password_proof,omitempty"`
		}{id, string(hash)})
		if _, err = tx.Exec(ctx, "INSERT INTO admin_setting_operations(actor_id,operation,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,$5)", actorID, operation, key, requestHash, result); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit(ctx)
}
