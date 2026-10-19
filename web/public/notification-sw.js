/* Notifications only: no fetch handler, application cache or arbitrary payload URLs. */
self.addEventListener("push", function (event) {
  let destination = "/console/account";
  try {
    if (event.data && event.data.json().url === "/admin/account") destination = "/admin/account";
  } catch {}
  event.waitUntil(self.registration.showNotification("账户通知", {
    body: "您有新的账户通知，请登录查看。",
    icon: "/assets/notification-icon-192.png",
    tag: destination === "/admin/account" ? "goidc-admin-notification" : "goidc-account-notification",
    data: { destination },
  }));
});

self.addEventListener("notificationclick", function (event) {
  event.notification.close();
  const path = event.notification.data && event.notification.data.destination === "/admin/account" ? "/admin/account" : "/console/account";
  const destination = new URL(path, self.location.origin).href;
  event.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(function (windows) {
    const existing = windows.find(function (client) { return client.url === destination; });
    if (existing) return existing.focus();
    return self.clients.openWindow(destination);
  }));
});
