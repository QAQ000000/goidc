package store

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

// SupplierHostSyncInput is the legacy downstream callback payload. The
// signature covers only id, token and rand_str, matching the PHP provider.
type SupplierHostSyncInput struct {
	ID            string `json:"id"`
	HostID        string `json:"host_id"`
	Domain        string `json:"domain"`
	DomainStatus  string `json:"domainstatus"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	DedicatedIP   string `json:"dedicatedip"`
	AssignedIPs   string `json:"assignedips"`
	Port          string `json:"port"`
	OS            string `json:"os"`
	OSURL         string `json:"os_url"`
	NextDueDate   string `json:"nextduedate"`
	SuspendReason string `json:"suspendreason"`
	Type          string `json:"type"`
	Signature     string `json:"signature"`
	RandStr       string `json:"rand_str"`
}

func supplierDownstreamToken(key []byte, serviceID int64) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("supplier-service:" + strconv.FormatInt(serviceID, 10)))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func supplierCallbackSignature(id, token, randStr string) string {
	data, _ := json.Marshal(map[string]string{"id": id, "rand_str": randStr, "token": token})
	sum := md5.Sum(data)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func parseSupplierCallbackID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id < 1 {
		return 0, conflict("supplier_callback_invalid", "供应商回调服务编号无效")
	}
	return id, nil
}

func (s *Store) SyncSupplierHost(ctx context.Context, in SupplierHostSyncInput) error {
	serviceID, err := parseSupplierCallbackID(in.ID)
	if err != nil {
		return err
	}
	hostID, err := parseSupplierCallbackID(in.HostID)
	if err != nil {
		return err
	}
	if len(s.ProvisionEncryptionKey) != 32 || strings.TrimSpace(in.RandStr) == "" || !hmac.Equal([]byte(strings.ToUpper(strings.TrimSpace(in.Signature))), []byte(supplierCallbackSignature(in.ID, supplierDownstreamToken(s.ProvisionEncryptionKey, serviceID), in.RandStr))) {
		return conflict("supplier_callback_signature", "供应商回调签名无效")
	}
	port := 0
	if strings.TrimSpace(in.Port) != "" {
		port, err = strconv.Atoi(strings.TrimSpace(in.Port))
		if err != nil || port < 0 || port > 65535 {
			return conflict("supplier_callback_invalid", "供应商回调端口无效")
		}
	}
	for _, value := range []string{in.Domain, in.Username, in.DedicatedIP, in.AssignedIPs, in.OS, in.OSURL, in.SuspendReason} {
		if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return conflict("supplier_callback_invalid", "供应商回调字段无效")
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var mode, existing, current, snapshotSupplier string
	var supplierID, upstreamProductID int64
	if err = tx.QueryRow(ctx, `SELECT s.provision_mode,s.upstream_id,s.status,coalesce(s.delivery_snapshot->>'supplier_id',''),
 b.supplier_id,b.upstream_product_id
 FROM services s JOIN supplier_product_bindings b ON b.product_id=s.product_id
 WHERE s.id=$1 FOR UPDATE`, serviceID).Scan(&mode, &existing, &current, &snapshotSupplier, &supplierID, &upstreamProductID); err != nil {
		return err
	}
	if mode != "supplier" || snapshotSupplier != strconv.FormatInt(supplierID, 10) {
		return conflict("supplier_callback_invalid", "服务供应商绑定无效")
	}
	if existing != "" && existing != strconv.FormatInt(hostID, 10) {
		return conflict("supplier_resource_mismatch", "供应商资源与本地服务不匹配")
	}
	status := strings.TrimSpace(in.DomainStatus)
	if status == "" {
		status = current
	}
	remote := supplier.Service{UpstreamID: hostID, ProductID: upstreamProductID, Status: status, Domain: strings.TrimSpace(in.Domain), Username: strings.TrimSpace(in.Username), Password: html.UnescapeString(in.Password), DedicatedIP: strings.TrimSpace(in.DedicatedIP), AssignedIPs: strings.TrimSpace(in.AssignedIPs), Port: port, OS: strings.TrimSpace(in.OS), OSURL: strings.TrimSpace(in.OSURL), SuspendReason: strings.TrimSpace(in.SuspendReason), NextDueDate: strings.TrimSpace(in.NextDueDate)}
	if err = applySupplierRemoteSafely(ctx, tx, ProvisionJob{ServiceID: serviceID, Action: "sync"}, remote, s.ProvisionEncryptionKey); err != nil {
		return err
	}
	if current == "provisioning" && (status == "active" || status == "Active") {
		if err = s.QueueServiceProvisionSuccessNotifications(ctx, tx, serviceID); err != nil {
			return err
		}
	}
	if current == "active" && (status == "suspended" || status == "Suspended") {
		if err = s.QueueServiceSuspendedNotifications(ctx, tx, serviceID); err != nil {
			return err
		}
	}
	if err = Audit(ctx, tx, 0, "supplier.callback.synced", "service", serviceID, map[string]any{"upstream_id": hostID, "status": status, "type": strings.TrimSpace(in.Type)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readSupplierCredentials(ctx context.Context, tx pgx.Tx, serviceID int64, key []byte) (provision.Credentials, error) {
	var sealed []byte
	if err := tx.QueryRow(ctx, "SELECT credentials_encrypted FROM services WHERE id=$1", serviceID).Scan(&sealed); err != nil {
		return provision.Credentials{}, err
	}
	if len(sealed) == 0 {
		return provision.Credentials{}, nil
	}
	value, err := provision.Unseal(key, serviceID, sealed)
	if err != nil {
		return provision.Credentials{}, conflict("supplier_credentials_invalid", "服务凭据无法更新")
	}
	return value, nil
}

func supplierDueDate(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return nil, nil
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 1 && seconds <= 253402300799 {
		value := time.Unix(seconds, 0).UTC()
		return &value, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if value, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return &value, nil
		}
	}
	return nil, conflict("supplier_due_invalid", "上游到期时间不可识别，请核验")
}
