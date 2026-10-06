package provision

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DiskRates struct {
	Read  *float64 `json:"read_kbps"`
	Write *float64 `json:"write_kbps"`
}
type MonitoringSample struct {
	At         time.Time            `json:"at"`
	CPU        *float64             `json:"cpu_percent"`
	NetworkIn  *float64             `json:"network_in_kbps"`
	NetworkOut *float64             `json:"network_out_kbps"`
	Disks      map[string]DiskRates `json:"disks"`
}
type TrafficUsage struct {
	In    string  `json:"in_bytes"`
	Out   string  `json:"out_bytes"`
	Total string  `json:"total_bytes"`
	Limit *string `json:"limit_bytes"`
}
type Monitoring struct {
	Start          time.Time          `json:"start"`
	End            time.Time          `json:"end"`
	FetchedAt      time.Time          `json:"fetched_at"`
	Samples        []MonitoringSample `json:"samples"`
	Usage          *TrafficUsage      `json:"usage"`
	UsageAvailable bool               `json:"usage_available"`
}

func parseMonitoringTime(value string, location *time.Location) (time.Time, error) {
	if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return at.UTC(), nil
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", value, location)
	return at.UTC(), err
}

func monitoringJSON(value *string, out any) error {
	if value == nil || strings.TrimSpace(*value) == "" || strings.TrimSpace(*value) == "null" {
		return nil
	}
	raw := []byte(*value)
	if !unambiguousJSON(raw) || json.Unmarshal(raw, out) != nil {
		return &Error{Code: "invalid_monitoring_response"}
	}
	return nil
}
func validMonitoringRate(value *float64, max float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= max)
}

// NOKVM returns JSON strings inside its monitoring JSON envelope. Validate both
// layers and keep missing measurements as null, rather than manufacturing zeros.
func (n *NOKVM) Monitoring(ctx context.Context, id string, start, end time.Time) (Monitoring, error) {
	result := Monitoring{Start: start.UTC(), End: end.UTC(), Samples: []MonitoringSample{}}
	if !ValidResourceID(id) || start.IsZero() || !end.After(start) || end.Sub(start) > 7*24*time.Hour {
		return result, &Error{Code: "invalid_monitoring_range"}
	}
	var rows []struct {
		CreatedAt string  `json:"CreatedAt"`
		CPU       *string `json:"Cpu"`
		Disk      *string `json:"Disk"`
		Network   *string `json:"Network"`
	}
	if err := n.call(ctx, http.MethodGet, "/api/virtual_monitoring/"+id, nil, &rows); err != nil {
		return result, err
	}
	if len(rows) > 12000 {
		return result, &Error{Code: "invalid_monitoring_response"}
	}
	samples := map[time.Time]MonitoringSample{}
	for _, row := range rows {
		at, err := parseMonitoringTime(row.CreatedAt, n.monitoringLocation)
		if err != nil {
			return result, &Error{Code: "invalid_monitoring_timestamp"}
		}
		if at.Before(start) || at.After(end) {
			continue
		}
		point := MonitoringSample{At: at, Disks: map[string]DiskRates{}}
		var cpu struct {
			Value *float64 `json:"value"`
		}
		var network struct {
			In  *float64 `json:"in"`
			Out *float64 `json:"out"`
		}
		var disks map[string][]*float64
		if monitoringJSON(row.CPU, &cpu) != nil || monitoringJSON(row.Network, &network) != nil || monitoringJSON(row.Disk, &disks) != nil {
			return result, &Error{Code: "invalid_monitoring_response"}
		}
		// Some suppliers report aggregate core percentages. Preserve that value.
		if !validMonitoringRate(cpu.Value, 102400) || !validMonitoringRate(network.In, 1e12) || !validMonitoringRate(network.Out, 1e12) {
			return result, &Error{Code: "invalid_monitoring_measurement"}
		}
		point.CPU, point.NetworkIn, point.NetworkOut = cpu.Value, network.In, network.Out
		for _, disk := range []string{"vda", "vdb"} {
			if rates, ok := disks[disk]; ok {
				if len(rates) != 2 || !validMonitoringRate(rates[0], 1e12) || !validMonitoringRate(rates[1], 1e12) {
					return result, &Error{Code: "invalid_monitoring_measurement"}
				}
				point.Disks[disk] = DiskRates{Read: rates[0], Write: rates[1]}
			}
		}
		if previous, exists := samples[at]; exists && !reflect.DeepEqual(previous, point) {
			return result, &Error{Code: "ambiguous_monitoring_sample"}
		}
		samples[at] = point
	}
	for _, point := range samples {
		result.Samples = append(result.Samples, point)
	}
	sort.Slice(result.Samples, func(i, j int) bool { return result.Samples[i].At.Before(result.Samples[j].At) })
	// Usage is independent of the requested chart window; it is the supplier's
	// cumulative counter. A failed usage read must not discard valid chart samples.
	var resource struct {
		Flow *struct {
			Code *int `json:"code"`
			Data struct {
				In  *int64 `json:"in"`
				Out *int64 `json:"out"`
			} `json:"data"`
		} `json:"flow"`
		Limit *int64 `json:"flow_limit"`
	}
	if n.call(ctx, http.MethodGet, "/api/virtual/"+id, nil, &resource) == nil && resource.Flow != nil && resource.Flow.Code != nil && *resource.Flow.Code == 0 && resource.Flow.Data.In != nil && resource.Flow.Data.Out != nil {
		in, out := *resource.Flow.Data.In, *resource.Flow.Data.Out
		if in >= 0 && out >= 0 && in <= math.MaxInt64-out {
			usage := &TrafficUsage{In: strconv.FormatInt(in, 10), Out: strconv.FormatInt(out, 10), Total: strconv.FormatInt(in+out, 10)}
			if resource.Limit != nil && *resource.Limit >= 0 && *resource.Limit <= 100000000 {
				limit := strconv.FormatInt(*resource.Limit*(1<<30), 10)
				usage.Limit = &limit
			}
			result.Usage, result.UsageAvailable = usage, true
		}
	}
	if ctx.Err() != nil {
		return result, &Error{Code: "monitoring_cancelled"}
	}
	result.FetchedAt = time.Now().UTC()
	return result, nil
}
