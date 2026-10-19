/* Notifications only: no fetch handler, application cache or payload URLs. */
self.addEventListener("push", function (event) {
  event.waitUntil(self.registration.showNotification("账户通知", {
    body: "您有新的账户通知，请登录查看。",
    icon: "/assets/notification-icon-192.png",
    tag: "goidc-account-notification",
    data: {},
  }));
});

self.addEventListener("notificationclick", function (event) {
  event.notification.close();
  const destination = new URL("/console/account", self.location.origin).href;
  event.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(function (windows) {
    const existing = windows.find(function (client) { return client.url === destination; });
    if (existing) return existing.focus();
    return self.clients.openWindow(destination);
  }));
});
