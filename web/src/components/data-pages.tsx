"use client";
import { AdminCustomerDetailView } from "./admin-customer-detail";
import { AdminCustomersView } from "./admin-customers";
import { AdminDashboard } from "./admin-dashboard";

export function DashboardView() {
  return <AdminDashboard />;
}

export function CustomersView() {
  return <AdminCustomersView />;
}

export function AdminCustomerContactsView({ customerId }: { customerId: number }) {
  return <AdminCustomerDetailView customerId={customerId} />;
}

export { AdminCustomerDetailView } from "./admin-customer-detail";

export { ReportsView } from "./report-pages";

export { OrdersView, SupplierOrdersView, OrderDetailView } from "./order-pages";

export { ServicesView, ServiceDetailView } from "./service-pages";

export { AccountView } from "./account-pages";

export { CustomerGroupsView, AdminListView, TicketsView, AuditView, CancellationsView } from "./admin-data-lists";
