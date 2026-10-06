package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Storage points expose only the identity and display fields used by the legacy
// snapshot/backups templates. Disk paths, supplier users and credentials stay upstream.
type StoragePoint struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type StorageRequest struct {
	Kind       string        `json:"kind"`
	Operation  string        `json:"operation"`
	PointID    string        `json:"point_id"`
	ProfileID  string        `json:"profile_id"`
	UpstreamID string        `json:"upstream_id"`
	Limit      int           `json:"limit"`
	Point      *StoragePoint `json:"point,omitempty"`
	Prepared   bool          `json:"-"`
}

func StorageActionParts(action string) (string, string, bool) {
	for _, kind := range []string{"snapshot", "backup"} {
		for _, op := range []string{"create", "delete", "restore"} {
			if action == kind+"_"+op {
				return kind, op, true
			}
		}
	}
	return "", "", false
}
func IsStorageAction(action string) bool { _, _, ok := StorageActionParts(action); return ok }
func IsStorageRestore(action string) bool {
	return action == "snapshot_restore" || action == "backup_restore"
}

func (n *NOKVM) ExecuteStorage(ctx context.Context, request StorageRequest) (Result, error) {
	if request.ProfileID != n.profile.ID || request.Limit < 1 || request.Limit > 100000000 {
		return Result{}, &Error{Code: "invalid_storage_request"}
	}
	if request.Operation == "create" {
		points, err := n.ListStorage(ctx, request.Kind, request.UpstreamID)
		if err != nil {
			return Result{}, &Error{Code: "storage_membership_unavailable"}
		}
		if len(points) >= request.Limit {
			return Result{}, &Error{Code: "storage_quota_reached"}
		}
	}
	if request.Operation == "restore" {
		state, err := n.powerStatus(ctx, request.UpstreamID)
		if err != nil {
			return Result{}, &Error{Code: "power_status_unavailable"}
		}
		if state != "on" && state != "off" {
			return Result{}, &Error{Code: "power_state_busy"}
		}
	}
	if request.Point != nil {
		points, err := n.ListStorage(ctx, request.Kind, request.UpstreamID)
		if err != nil {
			return Result{}, &Error{Code: "storage_membership_unavailable"}
		}
		found := false
		for _, point := range points {
			if point.ID == request.Point.ID && point.Name == request.Point.Name && point.CreatedAt.Equal(request.Point.CreatedAt) {
				found = true
				break
			}
		}
		if !found {
			return Result{}, &Error{Code: "storage_point_changed"}
		}
	}
	return n.StorageAction(ctx, request.Kind, request.Operation, request.UpstreamID, request.PointID)
}

func storagePath(kind string) (string, bool) {
	switch kind {
	case "snapshot":
		return "/api/snapshot", true
	case "backup":
		return "/api/backups", true
	default:
		return "", false
	}
}

// ListStorage reads the resource-scoped endpoint, not an account-wide directory.
func (n *NOKVM) ListStorage(ctx context.Context, kind, upstreamID string) ([]StoragePoint, error) {
	path, ok := storagePath(kind)
	if !ok || !ValidResourceID(upstreamID) {
		return nil, &Error{Code: "invalid_storage_request"}
	}
	var rows []struct {
		ID        json.Number `json:"id"`
		Name      string      `json:"name"`
		CreatedAt string      `json:"created_at"`
	}
	if err := n.call(ctx, http.MethodGet, path+"/"+upstreamID, nil, &rows); err != nil {
		return nil, err
	}
	if len(rows) > 1000 {
		return nil, &Error{Code: "invalid_storage_response"}
	}
	points := make([]StoragePoint, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		id := row.ID.String()
		at, err := parseMonitoringTime(row.CreatedAt, n.monitoringLocation)
		if !ValidResourceID(id) || seen[id] || err != nil || at.Year() < 2000 || at.Year() > 2200 ||
			strings.TrimSpace(row.Name) == "" || !utf8.ValidString(row.Name) || utf8.RuneCountInString(row.Name) > 255 || strings.IndexFunc(row.Name, unicode.IsControl) >= 0 {
			return nil, &Error{Code: "invalid_storage_response"}
		}
		seen[id] = true
		points = append(points, StoragePoint{ID: id, Name: row.Name, CreatedAt: at})
	}
	sort.Slice(points, func(i, j int) bool {
		if !points[i].CreatedAt.Equal(points[j].CreatedAt) {
			return points[i].CreatedAt.After(points[j].CreatedAt)
		}
		return points[i].ID < points[j].ID
	})
	return points, nil
}

// StorageAction must be called by a fenced worker task after local authorization
// and confirmation. A successful wire response acknowledges a command only;
// no completion/status protocol has been established for these endpoints.
func (n *NOKVM) StorageAction(ctx context.Context, kind, action, upstreamID, pointID string) (Result, error) {
	path, ok := storagePath(kind)
	if !ok || !ValidResourceID(upstreamID) ||
		(action != "create" && action != "delete" && action != "restore") ||
		(action == "create" && pointID != "") || (action != "create" && !ValidResourceID(pointID)) {
		return Result{}, &Error{Code: "invalid_storage_request"}
	}
	if action != "create" {
		points, err := n.ListStorage(ctx, kind, upstreamID)
		if err != nil {
			// No mutation was sent; a read failure is not an unknown write outcome.
			return Result{}, &Error{Code: "storage_membership_unavailable"}
		}
		found := false
		for _, point := range points {
			if point.ID == pointID {
				found = true
				break
			}
		}
		if !found {
			return Result{}, &Error{Code: "storage_point_not_found"}
		}
	}
	method := http.MethodPost
	var form url.Values
	switch action {
	case "create":
		path += "/"
		form = url.Values{"virtual": {upstreamID}}
	case "delete":
		path += "/" + pointID
		method = http.MethodDelete
	case "restore":
		path += "/" + pointID + "/edit"
		method = http.MethodGet
	}
	if ctx.Err() != nil {
		return Result{}, &Error{Code: "storage_cancelled"}
	}
	if err := n.call(ctx, method, path, form, nil); err != nil {
		return Result{}, err
	}
	return Result{UpstreamID: upstreamID, CommandAccepted: true}, &Error{Code: "storage_confirmation_pending", Unknown: true}
}
