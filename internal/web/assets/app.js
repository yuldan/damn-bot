// Dashboard shell: session principal + SSE stream. No framework, no bundler.
// Every visible message comes from the catalog through data-msg-* attributes,
// so this file carries no UI literal (GAP-INF-08).
const principal = document.querySelector("#principal");
const sessionState = document.querySelector("#session-state");
const eventsState = document.querySelector("#events-state");
const logout = document.querySelector("#logout");
const csrf = document.querySelector('meta[name="damnbot-csrf"]')?.content;

const msg = document.body.dataset;
const fill = (template, value) => (template ?? "").replace("%s", value);

fetch("/api/session", { headers: { Accept: "application/json" } })
  .then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
  .then((data) => {
    principal.textContent = data.principal;
    sessionState.textContent = fill(msg.msgSignedIn, data.principal);
  })
  .catch(() => {
    sessionState.textContent = msg.msgExpired ?? "";
  });

const stream = new EventSource("/api/events");
stream.addEventListener("hello", (event) => {
  const data = JSON.parse(event.data);
  eventsState.textContent = fill(msg.msgStreamOpen, data.principal);
});
stream.addEventListener("error", () => {
  eventsState.textContent = msg.msgReconnecting ?? "";
});

logout.addEventListener("click", () => {
  fetch("/api/logout", {
    method: "POST",
    headers: { "X-CSRF-Token": csrf ?? "" },
  }).then((res) => {
    if (res.ok) {
      location.replace(res.headers.get("X-End-Session") ?? "/auth");
    }
  });
});
