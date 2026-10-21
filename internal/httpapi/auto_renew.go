package httpapi

import "net/http"

func (s *Server) setAutoRenew(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Enabled                 *bool  `json:"enabled"`
		ExpectedCustomerID      *int64 `json:"expected_customer_id"`
		ExpectedOwnerGeneration *int64 `json:"expected_owner_generation"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_auto_renew", "请明确选择是否启用自动续费")
		return
	}
	guarded := input.ExpectedCustomerID != nil || input.ExpectedOwnerGeneration != nil
	if guarded && (!adminRoute(r) || input.ExpectedCustomerID == nil || input.ExpectedOwnerGeneration == nil || *input.ExpectedCustomerID <= 0 || *input.ExpectedOwnerGeneration < 0) {
		writeError(w, http.StatusBadRequest, "invalid_auto_renew", "管理员归属核验须同时提供有效客户和归属代次")
		return
	}
	var err error
	if guarded {
		err = s.db.SetAdminAutoRenewForOwner(requestFileContext(r), identityFrom(r).ID, id, *input.Enabled, *input.ExpectedCustomerID, *input.ExpectedOwnerGeneration)
	} else {
		err = s.db.SetAutoRenew(requestFileContext(r), identityFrom(r).ID, id, *input.Enabled, adminRoute(r))
	}
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"service_id": id, "auto_renew": *input.Enabled})
}
