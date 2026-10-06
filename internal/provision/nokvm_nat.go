package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type NATItem struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Type         string `json:"type,omitempty"`
	Domain       string `json:"domain,omitempty"`
	ExteriorPort int    `json:"exterior_port"`
	InteriorPort int    `json:"interior_port"`
}

type NATRequest struct {
	Kind         string   `json:"kind"`
	Operation    string   `json:"operation"`
	PointID      string   `json:"point_id"`
	Name         string   `json:"name,omitempty"`
	Type         string   `json:"type,omitempty"`
	Domain       string   `json:"domain,omitempty"`
	ExteriorPort int      `json:"exterior_port,omitempty"`
	InteriorPort int      `json:"interior_port,omitempty"`
	ProfileID    string   `json:"profile_id"`
	UpstreamID   string   `json:"upstream_id"`
	Limit        int      `json:"limit"`
	Point        *NATItem `json:"point,omitempty"`
	Prepared     bool     `json:"-"`
}

func IsNATAction(action string) bool {
	switch action {
	case "nat_acl_create", "nat_acl_delete", "nat_web_create", "nat_web_delete":
		return true
	}
	return false
}

func natPath(kind string) (string, bool) {
	switch kind {
	case "acl":
		return "/api/nat_acl", true
	case "web":
		return "/api/nat_web", true
	}
	return "", false
}

var natDomain = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))+$`)

func ValidNATDomain(domain string) bool {
	return len(domain) <= 253 && natDomain.MatchString(domain) && !strings.Contains(domain, "..")
}

func ValidNATName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 255 && strings.IndexFunc(name, unicode.IsControl) < 0
}

func ValidNATRequest(r NATRequest) bool {
	if _, ok := natPath(r.Kind); !ok || (r.Operation != "create" && r.Operation != "delete") {
		return false
	}
	if r.Operation == "delete" {
		return ValidResourceID(r.PointID) && r.Name == "" && r.Type == "" && r.Domain == "" && r.ExteriorPort == 0 && r.InteriorPort == 0
	}
	if r.PointID != "" || r.InteriorPort < 1 || r.InteriorPort > 65535 {
		return false
	}
	if r.Kind == "acl" {
		return ValidNATName(r.Name) && (r.Type == "tcp" || r.Type == "udp" || r.Type == "all") && r.Domain == "" && r.ExteriorPort == 0
	}
	return r.Name == "" && r.Type == "" && ValidNATDomain(r.Domain) && r.ExteriorPort == 80
}

func (n *NOKVM) ListNAT(ctx context.Context, kind, upstreamID string) ([]NATItem, error) {
	path, ok := natPath(kind)
	if !ok || !ValidResourceID(upstreamID) {
		return nil, &Error{Code: "invalid_nat_request"}
	}
	var rows []struct {
		ID           json.Number `json:"id"`
		Name         string      `json:"name"`
		Type         string      `json:"type"`
		Domain       string      `json:"domain"`
		ExteriorPort int         `json:"exterior_port"`
		InteriorPort int         `json:"interior_port"`
	}
	if err := n.call(ctx, http.MethodGet, path+"/"+upstreamID, nil, &rows); err != nil {
		return nil, err
	}
	if rows == nil || len(rows) > 1000 {
		return nil, &Error{Code: "invalid_nat_response"}
	}
	items := make([]NATItem, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		item := NATItem{ID: row.ID.String(), Name: row.Name, Type: row.Type, Domain: row.Domain, ExteriorPort: row.ExteriorPort, InteriorPort: row.InteriorPort}
		if !ValidResourceID(item.ID) || seen[item.ID] || item.ExteriorPort < 1 || item.ExteriorPort > 65535 || item.InteriorPort < 1 || item.InteriorPort > 65535 ||
			(kind == "acl" && (!ValidNATName(item.Name) || (item.Type != "tcp" && item.Type != "udp" && item.Type != "all"))) ||
			(kind == "web" && !ValidNATDomain(item.Domain)) {
			return nil, &Error{Code: "invalid_nat_response"}
		}
		seen[item.ID] = true
		if kind == "acl" {
			item.Domain = ""
		} else {
			item.Name, item.Type = "", ""
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (n *NOKVM) ExecuteNAT(ctx context.Context, r NATRequest) (Result, error) {
	path, ok := natPath(r.Kind)
	if !ok || !ValidNATRequest(r) || !ValidResourceID(r.UpstreamID) || r.ProfileID != n.profile.ID || r.Limit < 1 || r.Limit > 100000000 {
		return Result{}, &Error{Code: "invalid_nat_request"}
	}
	items, err := n.ListNAT(ctx, r.Kind, r.UpstreamID)
	if err != nil {
		return Result{}, &Error{Code: "nat_membership_unavailable"}
	}
	if r.Operation == "create" {
		if len(items) >= r.Limit {
			return Result{}, &Error{Code: "nat_quota_reached"}
		}
	} else {
		found := false
		for _, item := range items {
			if r.Point != nil && item == *r.Point && item.ID == r.PointID {
				found = true
			}
		}
		if !found {
			return Result{}, &Error{Code: "nat_point_changed"}
		}
	}
	method := http.MethodPost
	var form url.Values
	if r.Operation == "create" {
		path += "/"
		form = url.Values{"virtual": {r.UpstreamID}, "interior_port": {strconv.Itoa(r.InteriorPort)}}
		if r.Kind == "acl" {
			form.Set("name", r.Name)
			form.Set("type", r.Type)
		} else {
			form.Set("domain", r.Domain)
			form.Set("exterior_port", strconv.Itoa(r.ExteriorPort))
			form.Set("exterior_type", "http")
			form.Set("interior_type", "http")
		}
	} else {
		method = http.MethodDelete
		path += "/" + r.PointID
	}
	if ctx.Err() != nil {
		return Result{}, &Error{Code: "nat_cancelled"}
	}
	if err := n.call(ctx, method, path, form, nil); err != nil {
		return Result{}, err
	}
	return Result{UpstreamID: r.UpstreamID, CommandAccepted: true}, &Error{Code: "nat_confirmation_pending", Unknown: true}
}
