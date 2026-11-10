// Dashboard shell: session principal + SSE stream. No framework, no bundler.
// Every visible message comes from the catalog through data-msg-* attributes,
// so this file carries no UI literal (GAP-INF-08).
const principal = document.querySelector("#principal");
const sessionState = document.querySelector("#session-state");
const providerState = document.querySelector("#provider-state");
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
    if (data.waiting_provider_s) {
      providerState.textContent = fill(msg.msgWaitingProvider, data.waiting_provider_s);
    }
  })
  .catch(() => {
    sessionState.textContent = msg.msgExpired ?? "";
  });

// The stream needs a one-use 30 s ticket (GAP-SEC-07); the session token
// itself never leaves the HttpOnly cookie.
fetch("/api/ticket", {
  method: "POST",
  headers: { "X-CSRF-Token": csrf ?? "" },
})
  .then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
  .then((data) => openStream(data.ticket))
  .catch(() => {
    eventsState.textContent = msg.msgReconnecting ?? "";
  });

function openStream(ticket) {
  const stream = new EventSource("/api/events?ticket=" + encodeURIComponent(ticket));
  stream.addEventListener("hello", (event) => {
    const data = JSON.parse(event.data);
    eventsState.textContent = fill(msg.msgStreamOpen, data.principal);
  });
  stream.addEventListener("error", () => {
    eventsState.textContent = msg.msgReconnecting ?? "";
  });
  return stream;
}

// Approval modal focus trap (GAP-UX-07): Tab cycles inside the dialog and
// Escape refuses, so keyboard-only users never lose the focus ring.
const dialog = document.querySelector("#approval");
const trap = () => {
  const buttons = dialog.querySelectorAll("button");
  if (buttons.length === 0) return;
  const first = buttons[0];
  const last = buttons[buttons.length - 1];
  dialog.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      dialog.close();
      return;
    }
    if (event.key !== "Tab") return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  });
};

// Streaming text is batched per sentence and appended as one semantic article
// block, never one live-region update per token.
function appendBlock(role, text) {
  const article = document.createElement("article");
  const heading = document.createElement("h3");
  heading.textContent = role;
  const body = document.createElement("p");
  body.textContent = text;
  article.appendChild(heading);
  article.appendChild(body);
  document.querySelector("#transcript").appendChild(article);
}

trap();

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
