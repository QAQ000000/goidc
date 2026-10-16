package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Server struct {
	db  *store.Store
	cfg config.Config
	log *slog.Logger
}

func New(db *store.Store, cfg config.Config, log *slog.Logger) *Server {
	db.SupplierDownstreamURL = cfg.PublicOrigin
	db.Provisioners = cfg.NOKVMProfiles
	db.ProvisionEncryptionKey = cfg.ProvisionEncryptionKey
	db.PurchaseFieldEncryptionKey = cfg.PurchaseFieldEncryptionKey
	return &Server{db, cfg, log}
}

type identity struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Email       string   `json:"email"`
	Name        string   `json:"display_name"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}
type ctxKey int

const userKey ctxKey = 1
const apiKeyAuthKey ctxKey = 2

func identityFrom(r *http.Request) identity { return r.Context().Value(userKey).(identity) }
func bearerCredential(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], "zjmfgo_") || len(parts[1]) != 71 {
		return ""
	}
	return parts[1]
}
func apiKeyRequest(r *http.Request) bool {
	value, _ := r.Context().Value(apiKeyAuthKey).(bool)
	return value
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("POST /api/host/sync", s.supplierHostSync)
	mux.HandleFunc("POST /api/v1/host/sync", s.supplierHostSync)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/captcha", s.issueCaptcha)
	mux.HandleFunc("POST /api/v1/auth/login/mfa", s.loginMFA)
	mux.HandleFunc("GET /api/v1/auth/registration-fields", s.registrationFields)
	mux.HandleFunc("POST /api/v1/auth/register", s.register)
	mux.HandleFunc("GET /api/v1/auth/verification/channels", s.verificationChannels)
	mux.HandleFunc("POST /api/v1/auth/verification/request", s.requestRegistrationVerification)
	mux.HandleFunc("POST /api/v1/account/verification/request", s.sessionAuth(s.requestAccountVerification, false))
	mux.HandleFunc("POST /api/v1/account/verification/confirm", s.sessionAuth(s.confirmAccountVerification, false))
	mux.HandleFunc("POST /api/v1/auth/password-reset/request", s.requestPasswordReset)
	mux.HandleFunc("POST /api/v1/auth/password-reset/confirm", s.confirmPasswordReset)
	mux.HandleFunc("POST /api/v1/auth/logout", s.auth(s.logout, false))
	mux.HandleFunc("GET /api/v1/auth/impersonation", s.auth(s.impersonationStatus, false))
	mux.HandleFunc("POST /api/v1/auth/impersonation/exit", s.exitImpersonation)
	mux.HandleFunc("GET /api/v1/me", s.auth(s.me, false))
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/affiliate/click/{code}", s.affiliateClick)
	mux.HandleFunc("GET /api/v1/catalog/page", s.catalogPage)
	mux.HandleFunc("GET /api/v1/catalog/sections", s.catalogSections)
	mux.HandleFunc("GET /api/v1/site", s.publicSite)
	mux.HandleFunc("GET /api/v1/public-index", s.publicIndex)
	mux.HandleFunc("GET /api/v1/public-content/{kind}/{id}", s.publicContentPreview)
	mux.HandleFunc("GET /api/v1/content-images/{id}", s.contentImage)
	mux.HandleFunc("GET /api/v1/admin/site-settings", s.sessionAuth(s.siteSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/site-settings", s.sessionAuth(s.updateSiteSettings, true))
	mux.HandleFunc("GET /api/v1/admin/auth-settings", s.sessionAuth(s.adminAuthSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/auth-settings", s.sessionAuth(s.adminAuthSettings, true))
	mux.HandleFunc("GET /api/v1/admin/security/mfa", s.sessionAuth(s.adminMFASettings, true))
	mux.HandleFunc("PUT /api/v1/admin/security/mfa", s.sessionAuth(s.adminMFASettings, true))
	mux.HandleFunc("GET /api/v1/admin/theme-settings", s.auth(s.themeSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/theme-settings", s.auth(s.themeSettings, true))
	mux.HandleFunc("GET /api/v1/admin/site-fields", s.auth(s.siteFields, true))
	mux.HandleFunc("POST /api/v1/admin/site-fields", s.auth(s.siteFields, true))
	mux.HandleFunc("PATCH /api/v1/admin/site-fields/{id}", s.auth(s.siteFields, true))
	mux.HandleFunc("DELETE /api/v1/admin/site-fields/{id}", s.auth(s.siteFields, true))
	mux.HandleFunc("GET /api/v1/admin/navigation", s.auth(s.siteNavigation, true))
	mux.HandleFunc("POST /api/v1/admin/navigation", s.auth(s.siteNavigation, true))
	mux.HandleFunc("PATCH /api/v1/admin/navigation/{id}", s.auth(s.siteNavigation, true))
	mux.HandleFunc("DELETE /api/v1/admin/navigation/{id}", s.auth(s.siteNavigation, true))
	mux.HandleFunc("GET /api/v1/admin/friendly-links", s.auth(s.friendlyLinks, true))
	mux.HandleFunc("POST /api/v1/admin/friendly-links", s.auth(s.friendlyLinks, true))
	mux.HandleFunc("PATCH /api/v1/admin/friendly-links/{id}", s.auth(s.friendlyLinks, true))
	mux.HandleFunc("DELETE /api/v1/admin/friendly-links/{id}", s.auth(s.friendlyLinks, true))
	mux.HandleFunc("GET /api/v1/admin/system/runtime", s.sessionAuth(s.adminSystemRuntime, true))
	mux.HandleFunc("GET /api/v1/admin/system/about", s.sessionAuth(s.adminSystemAbout, true))
	mux.HandleFunc("GET /api/v1/admin/system/database", s.sessionAuth(s.adminSystemDatabase, true))
	mux.HandleFunc("GET /api/v1/admin/system/upgrade", s.sessionAuth(s.adminSystemUpgrade, true))
	mux.HandleFunc("GET /api/v1/admin/jobs", s.auth(s.adminJobs, true))
	mux.HandleFunc("GET /api/v1/admin/scheduler-settings", s.auth(s.schedulerSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/scheduler-settings", s.auth(s.updateSchedulerSettings, true))
	mux.HandleFunc("GET /api/v1/admin/jobs/queue", s.auth(s.adminJobQueue, true))
	mux.HandleFunc("GET /api/v1/admin/supplier-jobs", s.auth(s.supplierJobs, true))
	mux.HandleFunc("GET /api/v1/admin/supplier-tasks", s.auth(s.supplierJobs, true))
	mux.HandleFunc("POST /api/v1/admin/supplier-jobs/{id}/retry", s.auth(s.retrySupplierJob, true))
	mux.HandleFunc("GET /api/v1/admin/logs/jobs", s.auth(s.adminJobExecutionLog, true))
	mux.HandleFunc("GET /api/v1/admin/logs/jobs/trend", s.auth(s.adminSchedulerTrend, true))
	mux.HandleFunc("GET /api/v1/admin/logs/admin-login", s.auth(s.adminLoginLog, true))
	mux.HandleFunc("POST /api/v1/admin/logs/cleanup", s.auth(s.cleanupLogs, true))
	mux.HandleFunc("GET /api/v1/admin/blacklist", s.auth(s.adminBlacklist, true))
	mux.HandleFunc("POST /api/v1/admin/blacklist", s.auth(s.saveBlacklist, true))
	mux.HandleFunc("PATCH /api/v1/admin/blacklist/{id}", s.auth(s.saveBlacklist, true))
	mux.HandleFunc("DELETE /api/v1/admin/blacklist/{id}", s.auth(s.deleteBlacklist, true))
	mux.HandleFunc("GET /api/v1/admin/logs/sms", s.auth(s.adminSMSLog, true))
	mux.HandleFunc("GET /api/v1/admin/logs/inbox", s.auth(s.adminInboxLog, true))
	mux.HandleFunc("GET /api/v1/admin/logs/api", s.auth(s.adminAPILog, true))
	mux.HandleFunc("GET /api/v1/admin/logs/email", s.auth(s.adminEmailLog, true))
	mux.HandleFunc("GET /api/v1/admin/marketing/options", s.auth(s.marketingAudienceOptions, true))
	mux.HandleFunc("POST /api/v1/admin/marketing/audience-count", s.auth(s.marketingAudienceCount, true))
	mux.HandleFunc("GET /api/v1/admin/marketing", s.auth(s.marketingCampaigns, true))
	mux.HandleFunc("POST /api/v1/admin/marketing", s.auth(s.createMarketingCampaign, true))
	mux.HandleFunc("GET /api/v1/admin/marketing/{id}", s.auth(s.marketingCampaignDetail, true))
	mux.HandleFunc("PATCH /api/v1/admin/marketing/{id}", s.auth(s.updateMarketingCampaign, true))
	mux.HandleFunc("POST /api/v1/admin/marketing/{id}/send", s.auth(s.sendMarketingCampaign, true))
	mux.HandleFunc("GET /api/v1/admin/order-settings", s.sessionAuth(s.orderSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/order-settings", s.sessionAuth(s.updateOrderSettings, true))
	mux.HandleFunc("GET /api/v1/admin/payment-settings", s.sessionAuth(s.paymentSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/payment-settings", s.sessionAuth(s.updatePaymentSettings, true))
	mux.HandleFunc("GET /api/v1/credit/account", s.customerCreditAuth(s.customerCreditAccount))
	mux.HandleFunc("GET /api/v1/credit/consumptions", s.customerCreditAuth(s.customerCreditConsumptions))
	mux.HandleFunc("GET /api/v1/credit/repayments", s.customerCreditAuth(s.customerCreditRepayments))
	mux.HandleFunc("GET /api/v1/invoices/{id}/credit", s.customerCreditAuth(s.customerInvoiceCredit))
	mux.HandleFunc("GET /api/v1/admin/credit-settings", s.sessionAuth(s.creditSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/credit-settings", s.sessionAuth(s.updateCreditSettings, true))
	mux.HandleFunc("POST /api/v1/invoices/{id}/pay-credit", s.sessionAuth(s.payCreditInvoice, false))
	mux.HandleFunc("GET /api/v1/admin/credit/consumptions", s.auth(s.creditConsumptions, true))
	mux.HandleFunc("GET /api/v1/admin/credit/repayments", s.auth(s.creditRepayments, true))
	mux.HandleFunc("POST /api/v1/admin/credit/consumptions/{id}/repay", s.sessionAuth(s.creditRepay, true))
	mux.HandleFunc("PUT /api/v1/admin/credit/{id}/freeze", s.sessionAuth(s.creditFreeze, true))
	mux.HandleFunc("GET /api/v1/admin/credit", s.auth(s.adminCreditPage, true))
	mux.HandleFunc("PUT /api/v1/admin/credit/{id}/limit", s.sessionAuth(s.adminCreditLimit, true))
	mux.HandleFunc("GET /api/v1/admin/currencies", s.sessionAuth(s.currencies, true))
	mux.HandleFunc("POST /api/v1/admin/currencies", s.sessionAuth(s.saveCurrency, true))
	mux.HandleFunc("PATCH /api/v1/admin/currencies/{id}", s.sessionAuth(s.saveCurrency, true))
	mux.HandleFunc("DELETE /api/v1/admin/currencies/{id}", s.sessionAuth(s.deleteCurrency, true))
	mux.HandleFunc("POST /api/v1/admin/currencies/{id}/default", s.sessionAuth(s.setCurrencyDefault, true))
	mux.HandleFunc("GET /api/v1/admin/finance-settings", s.sessionAuth(s.financeSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/finance-settings", s.sessionAuth(s.updateFinanceSettings, true))
	mux.HandleFunc("GET /api/v1/admin/invoice-settings", s.sessionAuth(s.invoiceSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/invoice-settings", s.sessionAuth(s.updateInvoiceSettings, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-shipping-methods", s.sessionAuth(s.saveInvoiceShippingMethod, true))
	mux.HandleFunc("PATCH /api/v1/admin/invoice-shipping-methods/{id}", s.sessionAuth(s.saveInvoiceShippingMethod, true))
	mux.HandleFunc("DELETE /api/v1/admin/invoice-shipping-methods/{id}", s.sessionAuth(s.deleteInvoiceShippingMethod, true))
	mux.HandleFunc("GET /api/v1/admin/contracts/available-templates", s.sessionAuth(s.contractRead, true))
	mux.HandleFunc("GET /api/v1/admin/contracts", s.sessionAuth(s.contractRead, true))
	mux.HandleFunc("GET /api/v1/admin/contracts/eligible-services", s.sessionAuth(s.contractRead, true))
	mux.HandleFunc("GET /api/v1/admin/contracts/{id}", s.sessionAuth(s.contractRead, true))
	mux.HandleFunc("GET /api/v1/admin/contracts/{id}/document", s.sessionAuth(s.contractRead, true))
	mux.HandleFunc("POST /api/v1/admin/contracts", s.sessionAuth(s.contractWrite, true))
	mux.HandleFunc("GET /api/v1/admin/contract-templates", s.sessionAuth(s.contractRead, true))
	mux.HandleFunc("GET /api/v1/admin/contract-templates/{id}", s.sessionAuth(s.contractRead, true))
	mux.HandleFunc("GET /api/v1/contracts", s.sessionAuth(s.contractRead, false))
	mux.HandleFunc("GET /api/v1/contracts/eligible-services", s.sessionAuth(s.contractRead, false))
	mux.HandleFunc("GET /api/v1/contracts/{id}", s.sessionAuth(s.contractRead, false))
	mux.HandleFunc("GET /api/v1/contracts/{id}/document", s.sessionAuth(s.contractRead, false))
	mux.HandleFunc("POST /api/v1/contracts", s.sessionAuth(s.contractWrite, false))
	mux.HandleFunc("GET /api/v1/contract-templates", s.sessionAuth(s.contractRead, false))
	mux.HandleFunc("POST /api/v1/admin/contract-templates", s.sessionAuth(s.contractWrite, true))
	mux.HandleFunc("PUT /api/v1/admin/contract-templates/{id}", s.sessionAuth(s.contractWrite, true))
	mux.HandleFunc("POST /api/v1/admin/contracts/{id}/review", s.sessionAuth(s.contractWrite, true))
	mux.HandleFunc("PUT /api/v1/admin/contracts/{id}/shipping", s.sessionAuth(s.contractWrite, true))
	mux.HandleFunc("POST /api/v1/admin/contracts/{id}/void", s.sessionAuth(s.contractWrite, true))
	mux.HandleFunc("POST /api/v1/admin/contracts/bulk-void", s.sessionAuth(s.contractWrite, true))
	mux.HandleFunc("POST /api/v1/contracts/{id}/submit", s.sessionAuth(s.contractWrite, false))
	mux.HandleFunc("GET /api/v1/admin/contract-settings", s.sessionAuth(s.contractSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/contract-settings", s.sessionAuth(s.updateContractSettings, true))
	mux.HandleFunc("GET /api/v1/admin/certification-settings", s.sessionAuth(s.certificationSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/certification-settings", s.sessionAuth(s.updateCertificationSettings, true))
	mux.HandleFunc("GET /api/v1/admin/smtp-settings", s.sessionAuth(s.smtpSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/smtp-settings", s.sessionAuth(s.updateSMTPSettings, true))
	mux.HandleFunc("GET /api/v1/admin/sms-settings", s.sessionAuth(s.smsSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/sms-settings", s.sessionAuth(s.updateSMSSettings, true))
	mux.HandleFunc("POST /api/v1/admin/sms-settings/test", s.sessionAuth(s.testSMSSettings, true))
	mux.HandleFunc("POST /api/v1/admin/smtp-settings/test", s.sessionAuth(s.testSMTPSettings, true))
	mux.HandleFunc("GET /api/v1/admin/apps", s.sessionAuth(s.apps, true))
	mux.HandleFunc("GET /api/v1/admin/channel-catalogue", s.sessionAuth(s.channelCatalogue, true))
	mux.HandleFunc("GET /api/v1/admin/channel-instances", s.sessionAuth(s.channelInstances, true))
	mux.HandleFunc("POST /api/v1/admin/channel-instances", s.sessionAuth(s.createChannelInstance, true))
	mux.HandleFunc("PUT /api/v1/admin/channel-instances/{id}", s.sessionAuth(s.updateChannelInstance, true))
	mux.HandleFunc("DELETE /api/v1/admin/channel-instances/{id}", s.sessionAuth(s.deleteChannelInstance, true))
	mux.HandleFunc("POST /api/v1/admin/sales-settlements/preview", s.sessionAuth(s.salesSettlementPreview, true))
	mux.HandleFunc("POST /api/v1/admin/sales-settlements", s.sessionAuth(s.salesSettlementCreate, true))
	mux.HandleFunc("GET /api/v1/admin/sales-settlements", s.sessionAuth(s.salesSettlements, true))
	mux.HandleFunc("GET /api/v1/admin/sales-settlements/{id}", s.sessionAuth(s.salesSettlements, true))
	mux.HandleFunc("GET /api/v1/admin/sales-settings", s.sessionAuth(s.salesSettings, true))
	mux.HandleFunc("PUT /api/v1/admin/sales-settings", s.sessionAuth(s.updateSalesSettings, true))
	mux.HandleFunc("GET /api/v1/admin/sales-commission-groups", s.sessionAuth(s.salesCommissionGroups, true))
	mux.HandleFunc("POST /api/v1/admin/sales-commission-groups", s.sessionAuth(s.saveSalesCommissionGroup, true))
	mux.HandleFunc("PATCH /api/v1/admin/sales-commission-groups/{id}", s.sessionAuth(s.saveSalesCommissionGroup, true))
	mux.HandleFunc("DELETE /api/v1/admin/sales-commission-groups/{id}", s.sessionAuth(s.deleteSalesCommissionGroup, true))
	mux.HandleFunc("GET /api/v1/admin/sales-commission-tiers", s.sessionAuth(s.salesCommissionTiers, true))
	mux.HandleFunc("POST /api/v1/admin/sales-commission-tiers", s.sessionAuth(s.saveSalesCommissionTier, true))
	mux.HandleFunc("PATCH /api/v1/admin/sales-commission-tiers/{id}", s.sessionAuth(s.saveSalesCommissionTier, true))
	mux.HandleFunc("DELETE /api/v1/admin/sales-commission-tiers/{id}", s.sessionAuth(s.deleteSalesCommissionTier, true))
	mux.HandleFunc("GET /api/v1/admin/sales-staff", s.sessionAuth(s.salesStaff, true))
	mux.HandleFunc("PATCH /api/v1/admin/sales-staff/{id}", s.sessionAuth(s.updateSalesStaff, true))
	mux.HandleFunc("GET /api/v1/catalog/{id}", s.product)
	mux.HandleFunc("GET /api/v1/categories", s.categories)
	mux.HandleFunc("GET /api/v1/news", s.news)
	mux.HandleFunc("GET /api/v1/news/{id}", s.newsDetail)
	mux.HandleFunc("GET /api/v1/knowledgebase", s.knowledge)
	mux.HandleFunc("GET /api/v1/knowledgebase/{id}", s.knowledgeDetail)
	mux.HandleFunc("GET /api/v1/downloads", s.downloadsList)
	mux.HandleFunc("GET /api/v1/downloads/{id}/file", s.downloadFile)
	mux.HandleFunc("GET /api/v1/download-categories", s.downloadCategories)
	mux.HandleFunc("GET /api/v1/customer-files", s.auth(s.customerFiles, false))
	mux.HandleFunc("GET /api/v1/customer-files/{id}/file", s.auth(s.downloadCustomerFile, false))
	mux.HandleFunc("GET /api/v1/admin/customer-files", s.auth(s.customerFiles, true))
	mux.HandleFunc("GET /api/v1/admin/customer-files/owners", s.auth(s.customerFileOwners, true))
	mux.HandleFunc("GET /api/v1/admin/customer-files/{id}", s.auth(s.adminCustomerFileDetail, true))
	mux.HandleFunc("GET /api/v1/admin/customer-files/{id}/file", s.auth(s.downloadCustomerFile, true))
	mux.HandleFunc("POST /api/v1/admin/customer-files", s.auth(s.saveCustomerFile, true))
	mux.HandleFunc("PUT /api/v1/admin/customer-files/{id}", s.auth(s.saveCustomerFile, true))
	mux.HandleFunc("DELETE /api/v1/admin/customer-files/{id}", s.auth(s.deleteCustomerFiles, true))
	mux.HandleFunc("POST /api/v1/admin/customer-files/bulk-delete", s.auth(s.deleteCustomerFiles, true))
	mux.HandleFunc("GET /api/v1/admin/downloads", s.auth(s.downloadsList, true))
	mux.HandleFunc("GET /api/v1/admin/downloads/{id}", s.auth(s.adminDownloadDetail, true))
	mux.HandleFunc("POST /api/v1/admin/downloads", s.auth(s.saveDownload, true))
	mux.HandleFunc("PUT /api/v1/admin/downloads/{id}", s.auth(s.saveDownload, true))
	mux.HandleFunc("DELETE /api/v1/admin/downloads/{id}", s.auth(s.deleteDownload, true))
	mux.HandleFunc("GET /api/v1/admin/download-categories", s.auth(s.downloadCategories, true))
	mux.HandleFunc("POST /api/v1/admin/download-categories", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("PUT /api/v1/admin/download-categories/{id}", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("DELETE /api/v1/admin/download-categories/{id}", s.auth(s.deleteDownloadCategory, true))
	mux.HandleFunc("GET /api/v1/admin/file-groups", s.auth(s.adminFileGroups, true))
	mux.HandleFunc("POST /api/v1/admin/file-groups", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("PUT /api/v1/admin/file-groups/{id}", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("DELETE /api/v1/admin/file-groups/{id}", s.auth(s.deleteDownloadCategory, true))
	mux.HandleFunc("GET /api/v1/admin/download-settings", s.auth(s.downloadSettings, true))
	mux.HandleFunc("GET /api/v1/admin/download-products", s.auth(s.downloadProducts, true))
	mux.HandleFunc("PUT /api/v1/admin/download-settings", s.auth(s.downloadSettings, true))
	mux.HandleFunc("GET /api/v1/cart", s.auth(s.cart, false))
	mux.HandleFunc("POST /api/v1/cart/items", s.auth(s.addCartItem, false))
	mux.HandleFunc("PATCH /api/v1/cart/items/{id}", s.auth(s.updateCartItem, false))
	mux.HandleFunc("DELETE /api/v1/cart/items/{id}", s.auth(s.removeCartItem, false))
	mux.HandleFunc("POST /api/v1/cart/promo", s.auth(s.applyCartPromo, false))
	mux.HandleFunc("DELETE /api/v1/cart/promo", s.auth(s.removeCartPromo, false))
	mux.HandleFunc("POST /api/v1/orders", s.auth(s.createOrder, false))
	mux.HandleFunc("POST /api/v1/orders/{id}/cancel", s.auth(s.cancelOrder, false))
	mux.HandleFunc("GET /api/v1/orders", s.auth(s.orders, false))
	mux.HandleFunc("GET /api/v1/orders/page", s.auth(s.orders, false))
	mux.HandleFunc("GET /api/v1/orders/{id}", s.auth(s.customerOrderDetail, false))
	mux.HandleFunc("GET /api/v1/invoices", s.auth(s.invoices, false))
	mux.HandleFunc("GET /api/v1/invoices/page", s.auth(s.invoices, false))
	mux.HandleFunc("GET /api/v1/invoices/{id}", s.auth(s.invoiceDetail, false))
	mux.HandleFunc("GET /api/v1/account", s.auth(s.account, false))
	mux.HandleFunc("POST /api/v1/affiliate/withdrawals", s.auth(s.affiliateWithdrawals, false))
	mux.HandleFunc("GET /api/v1/affiliate/withdrawals", s.auth(s.affiliateWithdrawals, false))
	mux.HandleFunc("GET /api/v1/affiliate/withdrawals/{id}", s.auth(s.affiliateWithdrawalDetail, false))
	mux.HandleFunc("GET /api/v1/admin/affiliate/withdrawals", s.auth(s.adminAffiliateWithdrawals, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/withdrawals/{id}", s.auth(s.affiliateWithdrawalDetail, true))
	mux.HandleFunc("POST /api/v1/admin/affiliate/withdrawals/{id}/review", s.auth(s.reviewAffiliateWithdrawal, true))
	mux.HandleFunc("POST /api/v1/admin/affiliate/withdrawals/{id}/payout", s.auth(s.payoutAffiliateWithdrawal, true))
	mux.HandleFunc("POST /api/v1/admin/affiliate/withdrawals/{id}/target", s.auth(s.affiliatePayoutTarget, true))
	mux.HandleFunc("GET /api/v1/affiliate/overview", s.auth(s.affiliateOverview, false))
	mux.HandleFunc("GET /api/v1/affiliate/commissions", s.auth(s.affiliateCommissions, false))
	mux.HandleFunc("POST /api/v1/affiliate/apply", s.auth(s.applyAffiliate, false))
	mux.HandleFunc("GET /api/v1/affiliate/links", s.auth(s.affiliateLinks, false))
	mux.HandleFunc("POST /api/v1/affiliate/links", s.auth(s.affiliateLinks, false))
	mux.HandleFunc("PATCH /api/v1/affiliate/links/{id}", s.auth(s.updateAffiliateLink, false))
	mux.HandleFunc("GET /api/v1/account/certification", s.auth(s.certification, false))
	mux.HandleFunc("POST /api/v1/account/certification", s.auth(s.submitCertification, false))
	mux.HandleFunc("POST /api/v1/account/certification/convert-company", s.auth(s.convertCertificationToCompany, false))
	mux.HandleFunc("GET /api/v1/invoice-profiles", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("POST /api/v1/invoice-profiles", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("PUT /api/v1/invoice-profiles/{id}", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("DELETE /api/v1/invoice-profiles/{id}", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("GET /api/v1/invoice-addresses", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("POST /api/v1/invoice-addresses", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("PUT /api/v1/invoice-addresses/{id}", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("DELETE /api/v1/invoice-addresses/{id}", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("GET /api/v1/invoiceable-invoices", s.auth(s.invoiceableInvoices, false))
	mux.HandleFunc("GET /api/v1/invoice-applications", s.auth(s.invoiceApplications, false))
	mux.HandleFunc("GET /api/v1/invoice-applications/{id}", s.auth(s.invoiceApplicationDetail, false))
	mux.HandleFunc("GET /api/v1/invoice-applications/{id}/documents/{document_id}/file", s.auth(s.invoiceApplicationDocumentFile, false))
	mux.HandleFunc("POST /api/v1/invoice-applications", s.auth(s.createInvoiceApplication, false))
	mux.HandleFunc("GET /api/v1/admin/invoice-applications", s.auth(s.invoiceApplications, true))
	mux.HandleFunc("GET /api/v1/admin/invoice-applications/{id}", s.auth(s.invoiceApplicationDetail, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/review", s.auth(s.reviewInvoiceApplication, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/adjust", s.auth(s.adjustInvoiceApplication, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/document", s.auth(s.uploadInvoiceApplicationDocument, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/adjustments/{adjustment_id}/document", s.auth(s.uploadInvoiceApplicationDocument, true))
	mux.HandleFunc("GET /api/v1/admin/invoice-applications/{id}/documents/{document_id}/file", s.auth(s.invoiceApplicationDocumentFile, true))
	mux.HandleFunc("PUT /api/v1/account/password", s.auth(s.changePassword, false))
	mux.HandleFunc("GET /api/v1/account/mfa", s.sessionAuth(s.mfaStatus, false))
	mux.HandleFunc("POST /api/v1/account/mfa/enroll", s.sessionAuth(s.mfaEnroll, false))
	mux.HandleFunc("POST /api/v1/account/mfa/confirm", s.sessionAuth(s.mfaConfirm, false))
	mux.HandleFunc("POST /api/v1/account/mfa/disable", s.sessionAuth(s.mfaDisable, false))
	mux.HandleFunc("GET /api/v1/api-keys", s.sessionAuth(s.listAPIKeys, false))
	mux.HandleFunc("POST /api/v1/api-keys", s.sessionAuth(s.createAPIKey, false))
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", s.sessionAuth(s.revokeAPIKey, false))
	mux.HandleFunc("POST /api/v1/invoices/{id}/pay", s.auth(s.payInvoice, false))
	mux.HandleFunc("POST /api/v1/balance/topups/{id}/cancel", s.sessionAuth(s.cancelOnlineTopup, false))
	mux.HandleFunc("GET /api/v1/balance/topup-options", s.sessionAuth(s.topupOptions, false))
	mux.HandleFunc("POST /api/v1/balance/topups", s.sessionAuth(s.createOnlineTopup, false))
	mux.HandleFunc("POST /api/v1/invoices/{id}/payments", s.auth(s.createPayment, false))
	mux.HandleFunc("POST /api/v1/invoices/{id}/payments/{payment_id}/reconcile", s.auth(s.reconcilePayment, false))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/reconcile", s.auth(s.reconcilePayment, true))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payments/{payment_id}/close-verifications", s.paymentCloseHTTP(s.closeVerificationList))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/close-verifications", s.paymentCloseHTTP(s.closeVerificationWrite))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/close-verifications/{verification_id}/{action}", s.paymentCloseHTTP(s.closeVerificationReview))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payments/{payment_id}/close", s.paymentCloseHTTP(s.paymentCloseStatus))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payment-anomaly-groups", s.paymentCloseHTTP(s.paymentAnomalyGroups))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payment-anomaly-summary", s.paymentCloseHTTP(s.paymentAnomalySummary))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payment-anomaly-entries", s.paymentCloseHTTP(s.paymentAnomalyEntries))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payment-anomaly-groups/{anchor_id}/refunds", s.paymentCloseHTTP(s.createPaymentAnomalyRefund))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payment-anomaly-refunds/{refund_id}/confirm", s.paymentCloseHTTP(s.reviewPaymentAnomalyRefund))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payment-anomaly-refunds/{refund_id}/reject", s.paymentCloseHTTP(s.reviewPaymentAnomalyRefund))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/close", s.paymentCloseHTTP(s.closePayment))
	mux.HandleFunc("GET /api/v1/payment-providers", s.auth(s.paymentProviders, false))
	mux.HandleFunc("POST /api/v1/payments/{provider}/webhook", s.paymentWebhook)
	mux.HandleFunc("GET /api/v1/payments/{provider}/webhook", s.paymentWebhook)
	mux.HandleFunc("GET /api/v1/services", s.auth(s.services, false))
	mux.HandleFunc("GET /api/v1/services/page", s.auth(s.servicesPage, false))
	mux.HandleFunc("GET /api/v1/service-navigation", s.auth(s.serviceNavigation, false))
	mux.HandleFunc("PUT /api/v1/service-navigation/{id}", s.auth(s.setServiceNavigation, false))
	mux.HandleFunc("GET /api/v1/services/{id}", s.auth(s.serviceDetail, false))
	mux.HandleFunc("GET /api/v1/services/{id}/activity", s.auth(s.serviceActivity, false))
	mux.HandleFunc("POST /api/v1/services/{id}/vnc", s.auth(s.serviceVNC, false))
	mux.HandleFunc("POST /api/v1/services/{id}/credentials", s.auth(s.serviceCredentials, false))
	mux.HandleFunc("POST /api/v1/services/{id}/reset-password", s.auth(s.resetServicePassword, false))
	mux.HandleFunc("GET /api/v1/services/{id}/reinstall-templates", s.auth(s.reinstallTemplates, false))
	mux.HandleFunc("GET /api/v1/services/{id}/monitoring", s.auth(s.serviceMonitoring, false))
	mux.HandleFunc("GET /api/v1/services/{id}/storage/{kind}", s.auth(s.serviceStorage, false))
	mux.HandleFunc("POST /api/v1/services/{id}/storage/{kind}", s.auth(s.serviceStorage, false))
	mux.HandleFunc("GET /api/v1/services/{id}/nat/{kind}", s.auth(s.serviceNAT, false))
	mux.HandleFunc("POST /api/v1/services/{id}/nat/{kind}", s.auth(s.serviceNAT, false))
	mux.HandleFunc("GET /api/v1/services/{id}/cdrom", s.auth(s.serviceCDROM, false))
	mux.HandleFunc("POST /api/v1/services/{id}/cdrom", s.auth(s.serviceCDROM, false))
	mux.HandleFunc("POST /api/v1/services/{id}/reinstall", s.auth(s.reinstallService, false))
	mux.HandleFunc("GET /api/v1/services/{id}/downloads", s.auth(s.serviceDownloads, false))
	mux.HandleFunc("GET /api/v1/services/{id}/downloads/{download_id}/file", s.auth(s.serviceDownloadFile, false))
	mux.HandleFunc("POST /api/v1/services/{id}/actions", s.auth(s.serviceAction, false))
	mux.HandleFunc("POST /api/v1/services/{id}/renew", s.auth(s.renewService, false))
	mux.HandleFunc("GET /api/v1/services/{id}/traffic-products", s.auth(s.serviceTrafficProducts, false))
	mux.HandleFunc("POST /api/v1/services/{id}/traffic-orders", s.auth(s.buyTrafficProduct, false))
	mux.HandleFunc("GET /api/v1/services/{id}/product-upgrade-options", s.auth(s.productUpgradeOptions, false))
	mux.HandleFunc("POST /api/v1/services/{id}/product-quotes", s.auth(s.quoteProductChange, false))
	mux.HandleFunc("GET /api/v1/admin/products/{id}/upgrade-targets", s.auth(s.productUpgradeTargets, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/upgrade-targets", s.auth(s.productUpgradeTargets, true))
	mux.HandleFunc("GET /api/v1/services/{id}/upgrade-options", s.auth(s.configurationUpgradeOptions, false))
	mux.HandleFunc("POST /api/v1/services/{id}/configuration-quotes", s.auth(s.quoteConfigurationChange, false))
	mux.HandleFunc("POST /api/v1/services/{id}/billing-cycle-quotes", s.auth(s.quoteBillingCycleChange, false))
	mux.HandleFunc("POST /api/v1/services/{id}/configuration-changes", s.auth(s.submitConfigurationChange, false))
	mux.HandleFunc("DELETE /api/v1/services/{id}/configuration-changes/{change_id}", s.auth(s.cancelConfigurationChange, false))
	mux.HandleFunc("PATCH /api/v1/services/{id}/auto-renew", s.auth(s.setAutoRenew, false))
	mux.HandleFunc("POST /api/v1/services/{id}/cancellation", s.auth(s.requestCancellation, false))
	mux.HandleFunc("DELETE /api/v1/services/{id}/cancellation", s.auth(s.withdrawCancellation, false))
	mux.HandleFunc("GET /api/v1/me/ticket-notification-preferences", s.sessionAuth(s.ticketNotificationPreferences, false))
	mux.HandleFunc("PATCH /api/v1/me/ticket-notification-preferences", s.sessionAuth(s.ticketNotificationPreferences, false))
	mux.HandleFunc("GET /api/v1/tickets/{id}/notifications", s.auth(s.ticketNotifications, false))
	mux.HandleFunc("GET /api/v1/tickets", s.auth(s.tickets, false))
	mux.HandleFunc("GET /api/v1/ticket-departments", s.auth(s.ticketDepartments, false))
	mux.HandleFunc("POST /api/v1/tickets", s.auth(s.createTicket, false))
	mux.HandleFunc("GET /api/v1/tickets/{id}", s.auth(s.ticketDetail, false))
	mux.HandleFunc("POST /api/v1/tickets/{id}/attachments", s.auth(s.uploadTicketAttachment, false))
	mux.HandleFunc("GET /api/v1/tickets/{id}/attachments/{attachment_id}", s.auth(s.downloadTicketAttachment, false))
	mux.HandleFunc("DELETE /api/v1/tickets/{id}/attachments/{attachment_id}", s.auth(s.deleteTicketAttachment, false))
	mux.HandleFunc("POST /api/v1/tickets/{id}/replies", s.auth(s.replyTicket, false))
	mux.HandleFunc("PATCH /api/v1/tickets/{id}", s.auth(s.closeTicket, false))
	mux.HandleFunc("GET /api/v1/contacts", s.auth(s.contacts, false))
	mux.HandleFunc("POST /api/v1/contacts", s.auth(s.createContact, false))
	mux.HandleFunc("PATCH /api/v1/contacts/{id}", s.auth(s.updateContact, false))
	mux.HandleFunc("DELETE /api/v1/contacts/{id}", s.auth(s.deleteContact, false))
	mux.HandleFunc("GET /api/v1/admin/dashboard", s.auth(s.dashboard, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/purchase-fields", s.auth(s.savePurchaseFields, true))
	mux.HandleFunc("GET /api/v1/admin/permissions", s.sessionAuth(s.permissionCatalogue, true))
	mux.HandleFunc("GET /api/v1/admin/roles", s.sessionAuth(s.adminRoles, true))
	mux.HandleFunc("POST /api/v1/admin/roles", s.sessionAuth(s.saveAdminRole, true))
	mux.HandleFunc("PATCH /api/v1/admin/roles/{id}", s.sessionAuth(s.saveAdminRole, true))
	mux.HandleFunc("DELETE /api/v1/admin/roles/{id}", s.sessionAuth(s.deleteAdminRole, true))
	mux.HandleFunc("GET /api/v1/admin/staff", s.sessionAuth(s.adminStaff, true))
	mux.HandleFunc("GET /api/v1/admin/staff/{id}", s.sessionAuth(s.adminStaffDetail, true))
	mux.HandleFunc("POST /api/v1/admin/staff", s.sessionAuth(s.saveAdminStaff, true))
	mux.HandleFunc("PATCH /api/v1/admin/staff/{id}", s.sessionAuth(s.saveAdminStaff, true))
	mux.HandleFunc("GET /api/v1/admin/migrations/runs", s.auth(s.legacyImportRuns, true))
	mux.HandleFunc("GET /api/v1/admin/migrations/reviews", s.auth(s.legacyReviewList, true))
	mux.HandleFunc("GET /api/v1/admin/migrations/reviews/{entity}/{id}", s.auth(s.legacyReviewDetail, true))
	mux.HandleFunc("POST /api/v1/admin/migrations/reviews/{entity}/{id}", s.sessionAuth(s.reviewLegacy, true))
	mux.HandleFunc("GET /api/v1/admin/notification-templates/page", s.auth(s.notificationTemplatesPage, true))
	mux.HandleFunc("GET /api/v1/admin/notification-templates", s.auth(s.notificationTemplates, true))
	mux.HandleFunc("POST /api/v1/admin/notification-templates", s.auth(s.createNotificationTemplate, true))
	mux.HandleFunc("PATCH /api/v1/admin/notification-templates/{id}", s.auth(s.updateNotificationTemplate, true))
	mux.HandleFunc("GET /api/v1/admin/email-outbox", s.auth(s.emailOutbox, true))
	mux.HandleFunc("GET /api/v1/admin/logs", s.auth(s.adminLogPage, true))
	mux.HandleFunc("POST /api/v1/admin/email-outbox/{id}/retry", s.auth(s.retryEmail, true))
	mux.HandleFunc("GET /api/v1/admin/sms-outbox", s.auth(s.smsOutbox, true))
	mux.HandleFunc("POST /api/v1/admin/sms-outbox/{id}/retry", s.auth(s.retrySMS, true))
	mux.HandleFunc("GET /api/v1/admin/reports/income", s.auth(s.incomeReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/sales", s.auth(s.salesReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/tickets", s.auth(s.ticketReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/new-clients", s.auth(s.newClientsReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/customer-ranking", s.auth(s.customerRankingReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/product-income", s.auth(s.productIncomeReport, true))
	mux.HandleFunc("GET /api/v1/admin/products", s.auth(s.adminProducts, true))
	mux.HandleFunc("GET /api/v1/admin/products/page", s.auth(s.adminProductsPage, true))
	mux.HandleFunc("GET /api/v1/admin/supplier-providers", s.auth(s.supplierProviders, true))
	mux.HandleFunc("GET /api/v1/admin/supplier-api-settings", s.sessionAuth(s.supplierAPISettings, true))
	mux.HandleFunc("PUT /api/v1/admin/supplier-api-settings", s.sessionAuth(s.updateSupplierAPISettings, true))
	mux.HandleFunc("GET /api/v1/admin/suppliers", s.auth(s.suppliers, true))
	mux.HandleFunc("POST /api/v1/admin/suppliers", s.auth(s.saveSupplier, true))
	mux.HandleFunc("PUT /api/v1/admin/suppliers/{id}", s.auth(s.saveSupplier, true))
	mux.HandleFunc("POST /api/v1/admin/suppliers/{id}/test-connection", s.sessionAuth(s.testSupplierConnection, true))
	mux.HandleFunc("POST /api/v1/admin/suppliers/{id}/catalog", s.auth(s.supplierCatalog, true))
	mux.HandleFunc("GET /api/v1/admin/suppliers/{id}/products/{product_id}", s.auth(s.supplierProduct, true))
	mux.HandleFunc("POST /api/v1/admin/suppliers/{id}/import", s.auth(s.importSupplierProduct, true))
	mux.HandleFunc("GET /api/v1/admin/products/{id}/supplier-binding", s.auth(s.supplierBinding, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/supplier-verification", s.auth(s.supplierVerification, true))
	mux.HandleFunc("GET /api/v1/admin/provision-profiles", s.auth(s.provisionProfiles, true))
	mux.HandleFunc("GET /api/v1/admin/products/{id}", s.auth(s.adminProduct, true))
	mux.HandleFunc("PATCH /api/v1/admin/products/{id}", s.auth(s.createProduct, true))
	mux.HandleFunc("POST /api/v1/admin/products/{id}/duplicate", s.auth(s.duplicateProduct, true))
	mux.HandleFunc("GET /api/v1/admin/catalog-groups/{kind}", s.auth(s.adminCatalogGroups, true))
	mux.HandleFunc("GET /api/v1/admin/service-groups", s.auth(s.adminServiceGroups, true))
	mux.HandleFunc("POST /api/v1/admin/service-groups", s.auth(s.saveServiceGroup, true))
	mux.HandleFunc("PUT /api/v1/admin/service-groups/{id}", s.auth(s.saveServiceGroup, true))
	mux.HandleFunc("DELETE /api/v1/admin/service-groups/{id}", s.auth(s.deleteServiceGroup, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/service-group", s.auth(s.assignProductServiceGroup, true))
	mux.HandleFunc("POST /api/v1/admin/catalog-groups/{kind}", s.auth(s.saveCatalogGroup, true))
	mux.HandleFunc("PUT /api/v1/admin/catalog-groups/{kind}/{id}", s.auth(s.saveCatalogGroup, true))
	mux.HandleFunc("DELETE /api/v1/admin/catalog-groups/{kind}/{id}", s.auth(s.deleteCatalogGroup, true))
	mux.HandleFunc("POST /api/v1/admin/products", s.auth(s.createProduct, true))
	mux.HandleFunc("POST /api/v1/admin/products/{id}/pricing", s.auth(s.upsertProductPricing, true))
	mux.HandleFunc("GET /api/v1/admin/global-configuration-groups", s.auth(s.globalConfigurationGroups, true))
	mux.HandleFunc("GET /api/v1/admin/global-configuration-groups/{id}", s.auth(s.globalConfigurationDetail, true))
	mux.HandleFunc("POST /api/v1/admin/global-configuration-groups", s.auth(s.mutateGlobalConfiguration, true))
	mux.HandleFunc("POST /api/v1/admin/global-configuration-groups/copy", s.auth(s.mutateGlobalConfiguration, true))
	mux.HandleFunc("PUT /api/v1/admin/global-configuration-groups/{id}", s.auth(s.mutateGlobalConfiguration, true))
	mux.HandleFunc("PUT /api/v1/admin/global-configuration-groups/{id}/assignments", s.auth(s.mutateGlobalConfiguration, true))
	mux.HandleFunc("DELETE /api/v1/admin/global-configuration-groups/{id}", s.auth(s.mutateGlobalConfiguration, true))
	mux.HandleFunc("POST /api/v1/admin/products/{id}/option-groups", s.auth(s.saveConfigurationGroup, true))
	mux.HandleFunc("POST /api/v1/admin/products/{id}/configuration-rules", s.auth(s.saveNativeConfigurationRule, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/configuration-rules/{rule_id}", s.auth(s.saveNativeConfigurationRule, true))
	mux.HandleFunc("DELETE /api/v1/admin/products/{id}/configuration-rules/{rule_id}", s.auth(s.saveNativeConfigurationRule, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/option-groups/{group_id}", s.auth(s.saveConfigurationGroup, true))
	mux.HandleFunc("DELETE /api/v1/admin/products/{id}/option-groups/{group_id}", s.auth(s.saveConfigurationGroup, true))
	mux.HandleFunc("POST /api/v1/admin/products/{id}/option-groups/{group_id}/options", s.auth(s.saveConfigurationOption, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}", s.auth(s.saveConfigurationOption, true))
	mux.HandleFunc("DELETE /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}", s.auth(s.saveConfigurationOption, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}/price-tiers", s.auth(s.replaceOptionPriceTiers, true))
	mux.HandleFunc("POST /api/v1/admin/content", s.auth(s.adminCreateContent, true))
	mux.HandleFunc("GET /api/v1/admin/content", s.auth(s.adminContentList, true))
	mux.HandleFunc("GET /api/v1/admin/content/{id}", s.auth(s.adminContentDetail, true))
	mux.HandleFunc("PUT /api/v1/admin/content/{id}", s.auth(s.adminCreateContent, true))
	mux.HandleFunc("POST /api/v1/admin/content/{id}/images", s.auth(s.uploadContentImage, true))
	mux.HandleFunc("DELETE /api/v1/admin/content/{id}", s.auth(s.adminDeleteContent, true))
	mux.HandleFunc("GET /api/v1/admin/content-categories", s.auth(s.adminContentCategories, true))
	mux.HandleFunc("POST /api/v1/admin/content-categories", s.auth(s.adminSaveContentCategory, true))
	mux.HandleFunc("PUT /api/v1/admin/content-categories/{id}", s.auth(s.adminSaveContentCategory, true))
	mux.HandleFunc("DELETE /api/v1/admin/content-categories/{id}", s.auth(s.adminDeleteContentCategory, true))
	mux.HandleFunc("GET /api/v1/content-categories", s.publicContentCategories)
	mux.HandleFunc("GET /api/v1/content-tags", s.publicContentTags)
	mux.HandleFunc("GET /api/v1/admin/content-tags", s.auth(s.adminContentTags, true))
	mux.HandleFunc("POST /api/v1/admin/content-tags", s.auth(s.adminSaveContentTag, true))
	mux.HandleFunc("PUT /api/v1/admin/content-tags/{id}", s.auth(s.adminSaveContentTag, true))
	mux.HandleFunc("DELETE /api/v1/admin/content-tags/{id}", s.auth(s.adminDeleteContentTag, true))
	mux.HandleFunc("GET /api/v1/admin/customers", s.auth(s.customers, true))
	mux.HandleFunc("GET /api/v1/admin/customers/page", s.auth(s.customersPage, true))
	mux.HandleFunc("GET /api/v1/admin/customer-levels", s.auth(s.adminCustomerLevels, true))
	mux.HandleFunc("POST /api/v1/admin/customer-levels", s.auth(s.createCustomerLevel, true))
	mux.HandleFunc("PATCH /api/v1/admin/customer-levels/{id}", s.auth(s.updateCustomerLevel, true))
	mux.HandleFunc("DELETE /api/v1/admin/customer-levels/{id}", s.auth(s.deleteCustomerLevel, true))
	mux.HandleFunc("GET /api/v1/admin/certifications/page", s.auth(s.adminCertificationsPage, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/accounts/page", s.auth(s.adminAffiliateAccountsPage, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/attributions/page", s.auth(s.adminAffiliateAttributionsPage, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/withdrawals/page", s.auth(s.adminAffiliateWithdrawalsPage, true))
	mux.HandleFunc("POST /api/v1/admin/customers", s.auth(s.adminCreateCustomer, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/rules/schema", s.auth(s.adminAffiliateRulesSchema, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/rules", s.auth(s.adminAffiliateRules, true))
	mux.HandleFunc("PUT /api/v1/admin/affiliate/rules", s.auth(s.updateAdminAffiliateRules, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/policy", s.auth(s.adminAffiliatePolicy, true))
	mux.HandleFunc("PUT /api/v1/admin/affiliate/policy", s.auth(s.updateAdminAffiliatePolicy, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/accounts", s.auth(s.adminAffiliateAccounts, true))
	mux.HandleFunc("GET /api/v1/admin/affiliate/attributions", s.auth(s.adminAffiliateAttributions, true))
	mux.HandleFunc("POST /api/v1/admin/affiliate/accounts/{id}/status", s.auth(s.adminAffiliateAccountStatus, true))
	mux.HandleFunc("GET /api/v1/admin/certifications", s.auth(s.adminCertifications, true))
	mux.HandleFunc("GET /api/v1/admin/certifications/{id}", s.auth(s.adminCertificationDetail, true))
	mux.HandleFunc("GET /api/v1/admin/certifications/{id}/history", s.auth(s.adminCertificationHistory, true))
	mux.HandleFunc("POST /api/v1/admin/certifications/{id}/review", s.auth(s.reviewCertification, true))
	mux.HandleFunc("GET /api/v1/admin/customers/{id}", s.auth(s.adminCustomer, true))
	mux.HandleFunc("GET /api/v1/admin/customers/{id}/affiliate", s.auth(s.adminCustomerAffiliate, true))
	mux.HandleFunc("POST /api/v1/admin/customers/{id}/impersonate", s.auth(s.adminImpersonateCustomer, true))
	mux.HandleFunc("GET /api/v1/admin/transactions/summary", s.auth(s.adminTransactionSummary, true))
	mux.HandleFunc("GET /api/v1/admin/transactions/page", s.auth(s.adminTransactionEntries, true))
	mux.HandleFunc("GET /api/v1/admin/customer-transactions", s.auth(s.adminCustomerTransactions, true))
	mux.HandleFunc("GET /api/v1/admin/sales-owners", s.sessionAuth(s.salesOwners, true))
	mux.HandleFunc("PUT /api/v1/admin/customers/{id}/sales-owner", s.sessionAuth(s.assignCustomerSalesOwner, true))
	mux.HandleFunc("GET /api/v1/admin/orders/create-options", s.auth(s.adminOrderCreateOptions, true))
	mux.HandleFunc("POST /api/v1/admin/orders/quote", s.auth(s.adminOrderQuote, true))
	mux.HandleFunc("POST /api/v1/admin/orders", s.auth(s.createAdminOrder, true))
	mux.HandleFunc("POST /api/v1/admin/orders/{id}/invoice", s.auth(s.createDeferredOrderInvoice, true))
	mux.HandleFunc("DELETE /api/v1/admin/orders/{id}", s.auth(s.deleteOrder, true))
	mux.HandleFunc("POST /api/v1/admin/orders/bulk-review", s.auth(s.bulkReviewOrders, true))
	mux.HandleFunc("POST /api/v1/admin/orders/bulk-delete", s.auth(s.bulkDeleteOrders, true))
	mux.HandleFunc("GET /api/v1/admin/orders", s.auth(s.orders, true))
	mux.HandleFunc("GET /api/v1/admin/supplier-orders", s.auth(s.orders, true))
	mux.HandleFunc("GET /api/v1/admin/supplier-renewals", s.auth(s.renewalOrders, true))
	mux.HandleFunc("GET /api/v1/admin/orders/page", s.auth(s.orders, true))
	mux.HandleFunc("GET /api/v1/admin/renewal-orders", s.auth(s.renewalOrders, true))
	mux.HandleFunc("GET /api/v1/admin/traffic-products", s.auth(s.trafficProducts, true))
	mux.HandleFunc("POST /api/v1/admin/traffic-products", s.auth(s.trafficProducts, true))
	mux.HandleFunc("PATCH /api/v1/admin/traffic-products/{id}", s.auth(s.trafficProducts, true))
	mux.HandleFunc("DELETE /api/v1/admin/traffic-products/{id}", s.auth(s.trafficProducts, true))
	mux.HandleFunc("GET /api/v1/admin/traffic-orders", s.auth(s.trafficOrders, true))
	mux.HandleFunc("GET /api/v1/admin/traffic-orders/{id}", s.auth(s.trafficOrders, true))
	mux.HandleFunc("POST /api/v1/admin/traffic-orders/{id}/cancel", s.auth(s.trafficOrders, true))
	mux.HandleFunc("POST /api/v1/admin/traffic-orders/{id}/delivery-verification", s.auth(s.verifyTrafficDelivery, true))
	mux.HandleFunc("GET /api/v1/admin/orders/{id}", s.auth(s.adminOrderDetail, true))
	mux.HandleFunc("POST /api/v1/admin/orders/{id}/cancel", s.auth(s.cancelOrder, true))
	mux.HandleFunc("POST /api/v1/admin/orders/{id}/review", s.auth(s.reviewOrder, true))
	mux.HandleFunc("POST /api/v1/admin/orders/{id}/notes", s.auth(s.updateOrderNotes, true))
	mux.HandleFunc("POST /api/v1/admin/orders/commission", s.auth(s.adminOrderCommission, true))
	mux.HandleFunc("POST /api/v1/admin/orders/bulk-cancel", s.auth(s.bulkCancelOrders, true))
	mux.HandleFunc("GET /api/v1/admin/invoices", s.auth(s.invoices, true))
	mux.HandleFunc("GET /api/v1/admin/invoices/page", s.auth(s.invoices, true))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}", s.auth(s.invoiceDetail, true))
	mux.HandleFunc("GET /api/v1/admin/invoice-customers", s.auth(s.invoiceCustomers, true))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/topup-liability", s.topupLiabilityHTTP(s.topupLiability, false))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/topup-liability/resolve", s.topupLiabilityHTTP(s.resolveTopupLiability, true))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/external-refunds", s.sessionAuth(s.externalRefunds, true))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/external-refunds", s.sessionAuth(s.createExternalRefund, true))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/external-refunds/{refund_id}/confirm", s.sessionAuth(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("action", "confirm")
		s.reviewExternalRefund(w, r)
	}, true))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/external-refunds/{refund_id}/reject", s.sessionAuth(s.reviewExternalRefund, true))
	mux.HandleFunc("POST /api/v1/admin/invoices", s.auth(s.saveManualInvoice, true))
	mux.HandleFunc("PUT /api/v1/admin/invoices/{id}", s.auth(s.saveManualInvoice, true))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/cancel", s.auth(s.cancelManualInvoice, true))
	mux.HandleFunc("POST /api/v1/admin/invoices/merge", s.auth(s.mergeManualInvoices, true))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/refund", s.auth(s.adminRefundInvoice, true))
	mux.HandleFunc("POST /api/v1/admin/customers/{id}/balance", s.auth(s.adminTopUp, true))
	mux.HandleFunc("POST /api/v1/admin/customers/{id}/password", s.auth(s.adminSetCustomerPassword, true))
	mux.HandleFunc("PATCH /api/v1/admin/customers/{id}", s.auth(s.adminUpdateCustomer, true))
	mux.HandleFunc("GET /api/v1/admin/customers/{id}/contacts", s.auth(s.contacts, true))
	mux.HandleFunc("POST /api/v1/admin/customers/{id}/contacts", s.auth(s.createContact, true))
	mux.HandleFunc("PATCH /api/v1/admin/customers/{id}/contacts/{contact_id}", s.auth(s.updateContact, true))
	mux.HandleFunc("DELETE /api/v1/admin/customers/{id}/contacts/{contact_id}", s.auth(s.deleteContact, true))
	mux.HandleFunc("GET /api/v1/admin/customer-groups", s.auth(s.customerGroups, true))
	mux.HandleFunc("POST /api/v1/admin/customer-groups", s.auth(s.createCustomerGroup, true))
	mux.HandleFunc("PATCH /api/v1/admin/customer-groups/{id}", s.auth(s.updateCustomerGroup, true))
	mux.HandleFunc("DELETE /api/v1/admin/customer-groups/{id}", s.auth(s.deleteCustomerGroup, true))
	mux.HandleFunc("GET /api/v1/admin/customer-product-groups", s.auth(s.adminCustomerProductGroups, true))
	mux.HandleFunc("POST /api/v1/admin/customer-product-groups", s.auth(s.createCustomerProductGroup, true))
	mux.HandleFunc("PATCH /api/v1/admin/customer-product-groups/{id}", s.auth(s.updateCustomerProductGroup, true))
	mux.HandleFunc("DELETE /api/v1/admin/customer-product-groups/{id}", s.auth(s.deleteCustomerProductGroup, true))
	mux.HandleFunc("GET /api/v1/admin/customer-product-discounts", s.auth(s.adminCustomerProductDiscounts, true))
	mux.HandleFunc("PUT /api/v1/admin/customer-product-discounts", s.auth(s.saveCustomerProductDiscount, true))
	mux.HandleFunc("PUT /api/v1/admin/customers/{id}/group", s.auth(s.assignCustomerGroup, true))
	mux.HandleFunc("GET /api/v1/admin/customer-field-definitions", s.auth(s.customerFieldDefinitions, true))
	mux.HandleFunc("GET /api/v1/admin/customer-fields/{id}", s.auth(s.adminCustomerCustomField, true))
	mux.HandleFunc("GET /api/v1/admin/customer-fields", s.auth(s.adminCustomerCustomFields, true))
	mux.HandleFunc("POST /api/v1/admin/customer-fields", s.auth(s.createCustomerCustomField, true))
	mux.HandleFunc("PATCH /api/v1/admin/customer-fields/{id}", s.auth(s.updateCustomerCustomField, true))
	mux.HandleFunc("DELETE /api/v1/admin/customer-fields/{id}", s.auth(s.deleteCustomerCustomField, true))
	mux.HandleFunc("GET /api/v1/admin/promo-codes", s.auth(s.promoCodes, true))
	mux.HandleFunc("GET /api/v1/admin/promo-codes/page", s.auth(s.promoCodesPage, true))
	mux.HandleFunc("POST /api/v1/admin/promo-codes", s.auth(s.createPromoCode, true))
	mux.HandleFunc("PATCH /api/v1/admin/promo-codes/{id}", s.auth(s.updatePromoCode, true))
	mux.HandleFunc("GET /api/v1/admin/services", s.auth(s.services, true))
	mux.HandleFunc("GET /api/v1/admin/services/page", s.auth(s.servicesPage, true))
	mux.HandleFunc("GET /api/v1/admin/resources", s.auth(s.resourcesPage, true))
	mux.HandleFunc("GET /api/v1/admin/services/{id}", s.auth(s.serviceDetail, true))
	mux.HandleFunc("GET /api/v1/admin/services/{id}/activity", s.auth(s.serviceActivity, true))
	mux.HandleFunc("PUT /api/v1/admin/products/{id}/configuration-upgrade-policy", s.auth(s.saveConfigurationUpgradePolicy, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/configuration-changes/{change_id}/retry", s.auth(s.retryConfigurationChange, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/actions", s.auth(s.serviceAction, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/delivery", s.auth(s.serviceDelivery, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/delivery-verification", s.auth(s.verifyDelivery, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/credentials", s.auth(s.serviceCredentials, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/vnc", s.auth(s.serviceVNC, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/reset-password", s.auth(s.resetServicePassword, true))
	mux.HandleFunc("GET /api/v1/admin/services/{id}/reinstall-templates", s.auth(s.reinstallTemplates, true))
	mux.HandleFunc("GET /api/v1/admin/services/{id}/monitoring", s.auth(s.serviceMonitoring, true))
	mux.HandleFunc("GET /api/v1/admin/services/{id}/storage/{kind}", s.auth(s.serviceStorage, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/storage/{kind}", s.auth(s.serviceStorage, true))
	mux.HandleFunc("GET /api/v1/admin/services/{id}/nat/{kind}", s.auth(s.serviceNAT, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/nat/{kind}", s.auth(s.serviceNAT, true))
	mux.HandleFunc("GET /api/v1/admin/services/{id}/cdrom", s.auth(s.serviceCDROM, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/cdrom", s.auth(s.serviceCDROM, true))
	mux.HandleFunc("POST /api/v1/admin/services/{id}/reinstall", s.auth(s.reinstallService, true))
	mux.HandleFunc("GET /api/v1/admin/cancellations", s.auth(s.adminCancellations, true))
	mux.HandleFunc("POST /api/v1/admin/cancellations/{id}/decision", s.auth(s.adminResolveCancellation, true))
	mux.HandleFunc("GET /api/v1/admin/tickets", s.auth(s.tickets, true))
	mux.HandleFunc("GET /api/v1/admin/tickets/page", s.auth(s.adminTicketsPage, true))
	mux.HandleFunc("GET /api/v1/admin/tickets/create-options", s.auth(s.adminTicketCreateOptions, true))
	mux.HandleFunc("POST /api/v1/admin/tickets", s.auth(s.createAdminTicket, true))
	mux.HandleFunc("GET /api/v1/admin/ticket-statuses", s.auth(s.ticketStatuses, true))
	mux.HandleFunc("POST /api/v1/admin/ticket-statuses", s.auth(s.saveTicketStatus, true))
	mux.HandleFunc("PATCH /api/v1/admin/ticket-statuses/{id}", s.auth(s.updateTicketStatus, true))
	mux.HandleFunc("DELETE /api/v1/admin/ticket-statuses/{id}", s.auth(s.deleteTicketStatus, true))
	mux.HandleFunc("GET /api/v1/admin/ticket-rules", s.auth(s.ticketRules, true))
	mux.HandleFunc("POST /api/v1/admin/ticket-rules", s.auth(s.saveTicketRule, true))
	mux.HandleFunc("PATCH /api/v1/admin/ticket-rules/{id}", s.auth(s.updateTicketRule, true))
	mux.HandleFunc("DELETE /api/v1/admin/ticket-rules/{id}", s.auth(s.deleteTicketRule, true))
	mux.HandleFunc("GET /api/v1/admin/ticket-assignees", s.auth(s.ticketAssignees, true))
	mux.HandleFunc("GET /api/v1/admin/ticket-departments", s.auth(s.adminTicketDepartments, true))
	mux.HandleFunc("POST /api/v1/admin/ticket-departments", s.sessionAuth(s.createTicketDepartment, true))
	mux.HandleFunc("PATCH /api/v1/admin/ticket-departments/{id}", s.sessionAuth(s.updateTicketDepartment, true))
	mux.HandleFunc("DELETE /api/v1/admin/ticket-departments/{id}", s.sessionAuth(s.deleteTicketDepartment, true))
	mux.HandleFunc("PATCH /api/v1/admin/tickets/{id}/assignment", s.auth(s.assignTicket, true))
	mux.HandleFunc("POST /api/v1/admin/tickets/{id}/receive", s.auth(s.receiveTicket, true))
	mux.HandleFunc("POST /api/v1/admin/tickets/{id}/transfer", s.auth(s.transferTicket, true))
	mux.HandleFunc("POST /api/v1/admin/tickets/merge", s.auth(s.mergeTickets, true))
	mux.HandleFunc("GET /api/v1/admin/tickets/{id}/notes", s.auth(s.ticketNotes, true))
	mux.HandleFunc("POST /api/v1/admin/tickets/{id}/notes", s.auth(s.createTicketNote, true))
	mux.HandleFunc("DELETE /api/v1/admin/tickets/{id}/notes/{note_id}", s.auth(s.deleteTicketNote, true))
	mux.HandleFunc("GET /api/v1/admin/ticket-reply-presets", s.auth(s.ticketReplyPresets, true))
	mux.HandleFunc("GET /api/v1/admin/ticket-reply-preset-categories", s.auth(s.ticketReplyPresetCategories, true))
	mux.HandleFunc("POST /api/v1/admin/ticket-reply-preset-categories", s.auth(s.createTicketReplyPresetCategory, true))
	mux.HandleFunc("PATCH /api/v1/admin/ticket-reply-preset-categories/{id}", s.auth(s.updateTicketReplyPresetCategory, true))
	mux.HandleFunc("DELETE /api/v1/admin/ticket-reply-preset-categories/{id}", s.auth(s.deleteTicketReplyPresetCategory, true))
	mux.HandleFunc("POST /api/v1/admin/ticket-reply-presets", s.auth(s.createTicketReplyPreset, true))
	mux.HandleFunc("PATCH /api/v1/admin/ticket-reply-presets/{id}", s.auth(s.updateTicketReplyPreset, true))
	mux.HandleFunc("DELETE /api/v1/admin/ticket-reply-presets/{id}", s.auth(s.deleteTicketReplyPreset, true))
	mux.HandleFunc("GET /api/v1/admin/audit", s.auth(s.auditEvents, true))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		isWebhook := r.Method == "POST" && ((strings.HasPrefix(r.URL.Path, "/api/v1/payments/") && strings.HasSuffix(r.URL.Path, "/webhook")) || r.URL.Path == "/api/host/sync" || r.URL.Path == "/api/v1/host/sync")
		if !isWebhook && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && (bearerCredential(r) == "" || strings.HasPrefix(r.URL.Path, "/api/v1/auth/")) {
			if r.Header.Get("X-Requested-With") != "zjmfgo" {
				writeError(w, 403, "csrf_required", "请求校验失败")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
				writeError(w, 403, "origin_denied", "请求来源不允许")
				return
			}
		}
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("request panic", "path", r.URL.Path, "panic", v)
				writeError(w, 500, "internal_error", "请求处理失败")
			}
		}()
		if s.maintenanceAllowed(w, r) {
			mux.ServeHTTP(w, r)
		}
	})
}

func (s *Server) originAllowed(origin string) bool {
	if len(s.cfg.PublicOrigins) == 0 {
		return origin == s.cfg.PublicOrigin
	}
	for _, allowed := range s.cfg.PublicOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (s *Server) auth(next http.HandlerFunc, admin bool) http.HandlerFunc {
	return s.authWith(next, admin, false)
}
func (s *Server) sessionAuth(next http.HandlerFunc, admin bool) http.HandlerFunc {
	return s.authWith(next, admin, true)
}
func (s *Server) authWith(next http.HandlerFunc, admin, sessionOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bearer := bearerCredential(r)
		if sessionOnly && bearer != "" {
			writeError(w, http.StatusForbidden, "session_required", "该操作需要浏览器会话")
			return
		}
		var id identity
		var err error
		if bearer != "" {
			err = s.db.Pool.QueryRow(r.Context(), `SELECT u.id,u.username,u.email,u.display_name,u.role FROM api_keys k JOIN users u ON u.id=k.user_id
 WHERE k.secret_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.status='active'`, tokenHash(bearer)).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role)
			if err == nil {
				_, _ = s.db.Pool.Exec(r.Context(), "UPDATE api_keys SET last_used_at=now() WHERE secret_hash=$1", tokenHash(bearer))
			}
		} else {
			cookie, cookieErr := r.Cookie("zjmfgo_session")
			if cookieErr != nil {
				writeError(w, 401, "unauthorized", "请先登录")
				return
			}
			err = s.db.Pool.QueryRow(r.Context(), `SELECT u.id,u.username,u.email,u.display_name,u.role FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.expires_at>now() AND u.status='active'`, tokenHash(cookie.Value)).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 401, "unauthorized", "登录已失效")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if bearer != "" && id.Role == "customer" {
			var enabled bool
			if err = s.db.Pool.QueryRow(r.Context(), "SELECT enabled FROM resource_api_settings WHERE id=1").Scan(&enabled); err != nil {
				s.fail(w, r, err)
				return
			}
			if !enabled {
				writeError(w, http.StatusForbidden, "api_access_disabled", "客户 API 接口暂未开放")
				return
			}
		}
		if admin && id.Role != "admin" && id.Role != "super_admin" {
			writeError(w, 403, "forbidden", "需要管理员权限")
			return
		}
		id.Permissions = []string{}
		if id.Role == "admin" {
			id.Permissions, err = s.db.AdminGrants(r.Context(), id.ID)
			if err != nil {
				s.fail(w, r, err)
				return
			}
		} else if id.Role == "super_admin" {
			for _, p := range store.AdminPermissions {
				id.Permissions = append(id.Permissions, p.Code)
			}
		}
		if admin {
			permission, known := adminRoutePermissions[r.Pattern]
			allowed := id.can(permission)
			if r.Pattern == "GET /api/v1/admin/customer-field-definitions" {
				allowed = id.can("customers.view") || id.can("customers.manage")
			}
			if !known || (permission == "@super" && id.Role != "super_admin") || (permission != "@super" && !allowed) {
				writeError(w, 403, "permission_denied", "没有执行此操作的权限")
				return
			}
		}
		ctx := context.WithValue(r.Context(), userKey, id)
		if bearer != "" {
			ctx = context.WithValue(ctx, apiKeyAuthKey, true)
		}
		started := time.Now()
		recorder := &apiLogResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next(recorder, r.WithContext(ctx))
		// API logging is best effort and never changes the response that was
		// already delivered to the caller. Query/body secrets are intentionally
		// excluded; only the normalized route path is retained. The remote
		// address follows the same trusted-proxy rules as rate limits and
		// blacklists, so audit trails name the real client behind a reverse
		// proxy instead of the loopback hop.
		remoteIP := net.ParseIP(s.clientIP(r))
		_, _ = s.db.Pool.Exec(r.Context(), `INSERT INTO admin_api_logs(actor_id,method,path,status_code,duration_ms,remote_ip)
VALUES($1,$2,$3,$4,$5,$6)`, id.ID, r.Method, r.URL.Path, recorder.status, time.Since(started).Milliseconds(), remoteIP)
	}
}

type apiLogResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *apiLogResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *apiLogResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Pool.Ping(ctx); err != nil {
		writeError(w, 503, "database_unavailable", "数据库不可用")
		return
	}
	write(w, 200, map[string]string{"status": "ready", "database": "postgresql18"})
}
func write(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeErrorDetails(w, status, code, message, nil)
}
func writeErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	errorBody := map[string]any{"code": code, "message": message}
	for key, value := range details {
		errorBody[key] = value
	}
	json.NewEncoder(w).Encode(map[string]any{"error": errorBody})
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeLimit(w, r, dst, 64<<10)
}
func decodeLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		writeError(w, 415, "json_required", "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(dst); err != nil {
		writeError(w, 400, "invalid_json", "请求格式无效")
		return false
	}
	if err = d.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_json", "请求包含多余数据")
		return false
	}
	return true
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var c *store.Conflict
	if errors.As(err, &c) {
		if c.Code == "unauthorized" {
			writeError(w, 401, c.Code, c.Message)
			return
		}
		if c.Code == "permission_denied" || c.Code == "external_refund_self_review" {
			writeError(w, 403, c.Code, c.Message)
			return
		}
		writeError(w, 409, c.Code, c.Message)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found", "记录不存在")
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503" || pg.Code == "23514") {
		writeError(w, 409, "constraint_conflict", "记录重复或关联数据无效")
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, 500, "internal_error", "请求处理失败")
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_id", "记录编号无效")
		return 0, false
	}
	return id, true
}
func pagination(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 25
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
func (s *Server) list(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	rows, err := s.db.Pool.Query(r.Context(), query, args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, items)
}
