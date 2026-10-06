// Package provision implements the supplier wire protocol without depending on
// database, billing or HTTP API code. Errors deliberately exclude remote bodies
// and signed URLs; a request with an uncertain outcome must not be retried blindly.
package provision

import (
	"bytes"
	"context"
	"crypto/md5" // NOKVM's documented legacy signature, not password storage.
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Profile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	URL                string `json:"url"`
	Token              string `json:"token"`
	MonitoringTimezone string `json:"monitoring_timezone,omitempty"`
}

type Resources struct {
	AreaID     int64  `json:"areas_id"`
	Core       int    `json:"core"`
	CPUMode    string `json:"cpu_mode"`
	Memory     int    `json:"memory"`
	Disk       int    `json:"data_disk_size"`
	NetOut     int    `json:"net_out"`
	NetIn      int    `json:"net_in"`
	Snapshots  int    `json:"snapshoot"`
	Backups    int    `json:"backups"`
	TemplateID int64  `json:"templates_id"`
	IPNum      int    `json:"ip_num"`
	FlowLimit  int    `json:"flow_limit"`
	NATACL     int    `json:"nat_acl_limit"`
	NATWeb     int    `json:"nat_web_limit"`
	NodeID     int64  `json:"nodes_id"`
	Login      string `json:"login"`
}

func (r Resources) Validate() error {
	if r.AreaID < 1 || r.TemplateID < 1 || r.NodeID < 0 || r.Core < 1 || r.Core > 1024 || r.Memory < 128 || r.Memory > 16777216 || r.Disk < 0 || r.Disk > 1048576 || (r.Login != "root" && r.Login != "administrator") || len(r.CPUMode) > 64 || strings.ContainsAny(r.CPUMode, "\r\n\x00") {
		return &Error{Code: "invalid_resources"}
	}
	for _, n := range []int{r.NetOut, r.NetIn, r.Snapshots, r.Backups, r.IPNum, r.FlowLimit, r.NATACL, r.NATWeb} {
		if n < 0 || n > 100000000 {
			return &Error{Code: "invalid_resources"}
		}
	}
	return nil
}

type Delivery struct {
	ProfileID string     `json:"profile_id"`
	Resources Resources  `json:"resources"`
	Templates []Template `json:"templates,omitempty"`
}
type Template struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
	Login string `json:"login"`
}

func (t Template) Validate() error {
	if t.ID < 1 || t.ID > 2147483647 || strings.TrimSpace(t.Name) == "" || len(t.Name) > 120 || len(t.Group) > 80 || strings.ContainsAny(t.Name+t.Group, "\r\n\x00") || (t.Login != "root" && t.Login != "administrator") {
		return &Error{Code: "invalid_template"}
	}
	return nil
}

func (d Delivery) Validate() error {
	if err := d.Resources.Validate(); err != nil {
		return err
	}
	if len(d.Templates) > 100 {
		return &Error{Code: "invalid_templates"}
	}
	seen := map[int64]bool{}
	for _, t := range d.Templates {
		if t.Validate() != nil || seen[t.ID] {
			return &Error{Code: "invalid_templates"}
		}
		seen[t.ID] = true
	}
	return nil
}

type Credentials struct {
	Password    string `json:"password"`
	VNCPassword string `json:"vnc_password"`
}

func NewCredentials() (Credentials, error) {
	var c Credentials
	a, b := make([]byte, 16), make([]byte, 16)
	if _, err := rand.Read(a); err != nil {
		return c, err
	}
	if _, err := rand.Read(b); err != nil {
		return c, err
	}
	c.Password = "Zg!" + hex.EncodeToString(a)
	// The legacy module generates eight characters for QEMU's VNC limit.
	c.VNCPassword = base64.RawURLEncoding.EncodeToString(b[:6])
	return c, nil
}

type Request struct {
	Action      string
	UpstreamID  string
	UserID      int64
	ServiceID   int64
	Resources   Resources
	Credentials Credentials
	Template    Template
}
type Result struct {
	Credentials       Credentials `json:"-"`
	UpstreamID        string      `json:"upstream_id,omitempty"`
	Name              string      `json:"name,omitempty"`
	IPs               []string    `json:"ips,omitempty"`
	Power             string      `json:"power,omitempty"`
	CommandAccepted   bool        `json:"command_accepted,omitempty"`
	PasswordConfirmed bool        `json:"password_confirmed,omitempty"`
}
type Error struct {
	Code    string
	Unknown bool
}

func (e *Error) Error() string { return "NOKVM: " + e.Code }

type NOKVM struct {
	profile            Profile
	client             *http.Client
	monitoringLocation *time.Location
}

var profileID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var resourceID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func ValidResourceID(id string) bool {
	_, err := strconv.ParseInt(id, 10, 64)
	return resourceID.MatchString(id) && err == nil
}

func ValidSystemPassword(password string) bool {
	if len(password) < 12 || len(password) > 128 {
		return false
	}
	for _, ch := range []byte(password) {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}
func New(profile Profile) (*NOKVM, error) {
	u, err := url.Parse(profile.URL)
	if err != nil || !profileID.MatchString(profile.ID) || strings.TrimSpace(profile.Name) == "" || len(profile.Name) > 120 || strings.ContainsAny(profile.Name, "\r\n\x00") || profile.Token == "" || len(profile.Token) > 4096 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback())))) {
		return nil, &Error{Code: "invalid_profile"}
	}
	profile.URL = strings.TrimRight(profile.URL, "/")
	zone := profile.MonitoringTimezone
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	location, zoneErr := time.LoadLocation(zone)
	if zoneErr != nil || len(zone) > 100 {
		return nil, &Error{Code: "invalid_monitoring_timezone"}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Deployment proxy variables must never redirect internal supplier traffic.
	transport.Proxy = nil
	return &NOKVM{profile: profile, monitoringLocation: location, client: &http.Client{Timeout: 30 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (n *NOKVM) Summary() map[string]string {
	return map[string]string{"id": n.profile.ID, "name": n.profile.Name, "type": "nokvm"}
}

// Signature matches PHP sort(..., SORT_STRING), implode(), strtoupper(md5()).
func Signature(token, timestamp, random string) string {
	parts := []string{token, timestamp, random}
	sort.Strings(parts)
	sum := md5.Sum([]byte(strings.Join(parts, "")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
func (n *NOKVM) call(ctx context.Context, method, path string, form url.Values, data any) error {
	random := make([]byte, 3)
	if _, err := rand.Read(random); err != nil {
		return &Error{Code: "random_unavailable"}
	}
	rnd := hex.EncodeToString(random)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	query := url.Values{"time": {ts}, "random": {rnd}, "signature": {Signature(n.profile.Token, ts, rnd)}}
	req, err := http.NewRequestWithContext(ctx, method, n.profile.URL+path+"?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return &Error{Code: "invalid_request"}
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return &Error{Code: "transport_failure", Unknown: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &Error{Code: "http_failure", Unknown: true}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return &Error{Code: "invalid_response", Unknown: true}
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if !unambiguousJSON(raw) || json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil {
		return &Error{Code: "invalid_response", Unknown: true}
	}
	if *envelope.Code != 0 {
		return &Error{Code: "supplier_rejected"}
	}
	if data != nil && (len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, data) != nil) {
		return &Error{Code: "invalid_response", Unknown: true}
	}
	return nil
}

// Duplicate keys (including Go's case-insensitive field aliases) cannot safely
// establish whether a destructive supplier command was accepted or rejected.
func unambiguousJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 128 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, container := token.(json.Delim)
		if !container {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				name = strings.ToLower(name)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		end, err := d.Token()
		return err == nil && ((delim == '{' && end == json.Delim('}')) || (delim == '[' && end == json.Delim(']')))
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

type remoteResource struct {
	ID       json.Number `json:"id"`
	Name     string      `json:"name"`
	PublicIP []struct {
		IP string `json:"ip"`
	} `json:"public_ip"`
	IPList []struct {
		IP string `json:"ip"`
	} `json:"ip_list"`
}

func safeResource(remote remoteResource, id string) (Result, error) {
	if !ValidResourceID(id) || len(remote.Name) > 255 || strings.ContainsAny(remote.Name, "\r\n\x00") {
		return Result{}, &Error{Code: "invalid_resource", Unknown: true}
	}
	r := Result{UpstreamID: id, Name: remote.Name, IPs: []string{}}
	for _, v := range append(remote.PublicIP, remote.IPList...) {
		if ip := net.ParseIP(v.IP); ip != nil {
			r.IPs = append(r.IPs, ip.String())
		}
	}
	if len(r.IPs) > 256 {
		return Result{}, &Error{Code: "invalid_resource", Unknown: true}
	}
	return r, nil
}
func (n *NOKVM) Execute(ctx context.Context, request Request) (Result, error) {
	var remote remoteResource
	if request.Action == "create" {
		if request.UpstreamID != "" || request.ServiceID < 1 || request.UserID < 1 || len(request.Credentials.Password) < 12 || len(request.Credentials.VNCPassword) < 8 {
			return Result{}, &Error{Code: "invalid_create"}
		}
		if err := request.Resources.Validate(); err != nil {
			return Result{}, err
		}
		r := request.Resources
		form := url.Values{"users_id": {fmt.Sprint(request.UserID)}, "username": {fmt.Sprintf("zjmfgo-user-%d", request.UserID)}, "cpu_mode": {r.CPUMode}, "sys_pwd": {request.Credentials.Password}, "vnc_pwd": {request.Credentials.VNCPassword}, "expire_time": {"2999-01-01 00:00:00"}}
		for k, v := range map[string]int64{"areas_id": r.AreaID, "core": int64(r.Core), "memory": int64(r.Memory), "data_disk_size": int64(r.Disk), "net_out": int64(r.NetOut), "net_in": int64(r.NetIn), "snapshoot": int64(r.Snapshots), "backups": int64(r.Backups), "templates_id": r.TemplateID, "ip_num": int64(r.IPNum), "flow_limit": int64(r.FlowLimit), "nat_acl_limit": int64(r.NATACL), "nat_web_limit": int64(r.NATWeb)} {
			form.Set(k, fmt.Sprint(v))
		}
		if r.NodeID > 0 {
			form.Set("nodes_id", fmt.Sprint(r.NodeID))
		}
		if err := n.call(ctx, http.MethodPost, "/api/virtual", form, &remote); err != nil {
			return Result{}, err
		}
		return safeResource(remote, remote.ID.String())
	}
	if !ValidResourceID(request.UpstreamID) {
		return Result{}, &Error{Code: "invalid_resource_id"}
	}
	path := "/api/virtual/" + request.UpstreamID
	method := http.MethodGet
	switch request.Action {
	case "suspend":
		path = "/api/virtual_pause/" + request.UpstreamID
	case "resume":
		path = "/api/virtual_restore_pause/" + request.UpstreamID
	case "terminate":
		method = http.MethodDelete
	case "reset_password":
		if !ValidSystemPassword(request.Credentials.Password) {
			return Result{}, &Error{Code: "invalid_system_password"}
		}
		state, err := n.powerStatus(ctx, request.UpstreamID)
		if err != nil {
			return Result{}, &Error{Code: "power_status_unavailable"}
		}
		if state != "on" && state != "off" {
			return Result{}, &Error{Code: "power_state_busy"}
		}
		form := url.Values{"type": {"system"}, "password": {request.Credentials.Password}}
		if err := n.call(ctx, http.MethodPut, "/api/virtual_reset_password/"+request.UpstreamID, form, nil); err != nil {
			return Result{}, err
		}
		var confirmed struct {
			Password string `json:"sys_pwd"`
		}
		// The acknowledgement alone does not establish which credentials are current.
		if n.call(ctx, http.MethodGet, path, nil, &confirmed) != nil || subtle.ConstantTimeCompare([]byte(confirmed.Password), []byte(request.Credentials.Password)) != 1 {
			return Result{}, &Error{Code: "password_confirmation_pending", Unknown: true}
		}
		return Result{UpstreamID: request.UpstreamID, CommandAccepted: true, PasswordConfirmed: true}, nil
	case "reinstall":
		if err := request.Template.Validate(); err != nil {
			return Result{}, err
		}
		state, err := n.powerStatus(ctx, request.UpstreamID)
		if err != nil {
			return Result{}, &Error{Code: "power_status_unavailable"}
		}
		if state != "on" && state != "off" {
			return Result{}, &Error{Code: "power_state_busy"}
		}
		path = "/api/virtual_reset_system/" + request.UpstreamID + "/" + strconv.FormatInt(request.Template.ID, 10)
		if err := n.call(ctx, http.MethodGet, path, nil, nil); err != nil {
			return Result{}, err
		}
		// The legacy protocol acknowledges installation but exposes no proven
		// completion contract. Keep the task fenced until an administrator verifies it.
		return Result{UpstreamID: request.UpstreamID, CommandAccepted: true, Power: "processing"}, &Error{Code: "reinstall_confirmation_pending", Unknown: true}
	case "power_on", "power_off", "reboot", "force_off", "force_reboot":
		state, err := n.powerStatus(ctx, request.UpstreamID)
		if err != nil {
			// This read occurs before sending any command.
			return Result{}, &Error{Code: "power_status_unavailable"}
		}
		if state != "on" && state != "off" {
			return Result{}, &Error{Code: "power_state_busy"}
		}
		if request.Action != "power_on" && state != "on" {
			return Result{}, &Error{Code: "power_already_off"}
		}
		if request.Action == "power_on" && state == "on" {
			return Result{}, &Error{Code: "power_already_on"}
		}
		command := map[string]string{"power_on": "start", "power_off": "shut", "reboot": "restart", "force_off": "compel_shut", "force_reboot": "compel_restart"}[request.Action]
		if err := n.call(ctx, http.MethodGet, "/api/virtual_power/"+request.UpstreamID+"/"+command, nil, nil); err != nil {
			return Result{}, err
		}
		return Result{UpstreamID: request.UpstreamID, Power: "processing", CommandAccepted: true}, nil
	case "sync":
		if err := n.call(ctx, method, path, nil, &remote); err != nil {
			return Result{}, err
		}
		result, err := safeResource(remote, request.UpstreamID)
		if err != nil {
			return Result{}, err
		}
		result.Power, err = n.powerStatus(ctx, request.UpstreamID)
		if err != nil {
			return Result{}, err
		}
		return result, nil
	default:
		return Result{}, &Error{Code: "unsupported_action"}
	}
	if err := n.call(ctx, method, path, nil, nil); err != nil {
		return Result{}, err
	}
	return Result{UpstreamID: request.UpstreamID}, nil
}

func (n *NOKVM) powerStatus(ctx context.Context, id string) (string, error) {
	var state struct {
		Code *int `json:"code"`
	}
	if err := n.call(ctx, http.MethodGet, "/api/virtual_run_status/"+id, nil, &state); err != nil {
		return "", err
	}
	if state.Code == nil {
		return "", &Error{Code: "invalid_power_state", Unknown: true}
	}
	switch *state.Code {
	case 1, 12:
		return "on", nil
	case 2, 3:
		return "off", nil
	case 4, 5, 8, 11:
		return "processing", nil
	case 13:
		return "suspended", nil
	case 14:
		return "network_limited", nil
	default:
		return "", &Error{Code: "invalid_power_state", Unknown: true}
	}
}

func IsPowerAction(action string) bool {
	return action == "power_on" || action == "power_off" || action == "reboot" || action == "force_off" || action == "force_reboot"
}
