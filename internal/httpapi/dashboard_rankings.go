package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func dashboardRankingInput(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, int, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid_ranking_query", "排名查询参数无效")
		return time.Time{}, time.Time{}, 0, false
	}
	for name, entries := range values {
		if len(entries) != 1 || (name != "month" && name != "limit" && name != "format") {
			writeError(w, 400, "invalid_ranking_query", "排名查询参数无效")
			return time.Time{}, time.Time{}, 0, false
		}
	}
	if !reportJSONFormat(w, r) {
		return time.Time{}, time.Time{}, 0, false
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Now().In(zone)
	month := now.Format("2006-01")
	if entries, present := values["month"]; present {
		month = entries[0]
	}
	start, err := time.ParseInLocation("2006-01", month, zone)
	if err != nil || start.Format("2006-01") != month || start.Year() < 2000 || start.Year() > now.Year()+1 {
		writeError(w, 400, "invalid_report_period", "排名月份须为 YYYY-MM")
		return time.Time{}, time.Time{}, 0, false
	}
	limit := 5
	if entries, present := values["limit"]; present {
		limit, err = strconv.Atoi(entries[0])
		if err != nil || limit < 1 || limit > 20 {
			writeError(w, 400, "invalid_ranking_limit", "排名条数须为1至20")
			return time.Time{}, time.Time{}, 0, false
		}
	}
	return start, start.AddDate(0, 1, 0), limit, true
}

func (s *Server) productRankingReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	start, end, limit, ok := dashboardRankingInput(w, r)
	if !ok {
		return
	}
	result, err := s.db.ReportProductRanking(r.Context(), identityFrom(r).ID, start, end, limit)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) staffRankingReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	start, end, limit, ok := dashboardRankingInput(w, r)
	if !ok {
		return
	}
	result, err := s.db.ReportStaffRanking(r.Context(), identityFrom(r).ID, start, end, limit)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}
