// Dashboard shell: session principal + SSE stream. No framework, no bundler.
const principal = document.querySelector("#principal");
const sessionState = document.querySelector("#session-state");
const eventsState = document.querySelector("#events-state");
const logout = document.querySelector("#logout");
const csrf = document.querySelector('meta[name="damnbot-csrf"]')?.content;

fetch("/api/session", { headers: { Accept: "application/json" } })
  .then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
  .then((data) => {
    principal.textContent = data.principal;
    sessionState.textContent = `Signed in via ${data.auth}.`;
  })
  .catch(() => {
    sessionState.textContent = "Session expired.";
  });

const stream = new EventSource("/api/events");
stream.addEventListener("hello", (event) => {
  const data = JSON.parse(event.data);
  eventsState.textContent = `Stream open for ${data.principal}.`;
});
stream.addEventListener("error", () => {
  eventsState.textContent = "Stream reconnecting…";
});

logout.addEventListener("click", () => {
  fetch("/api/logout", {
    method: "POST",
    headers: { "X-CSRF-Token": csrf ?? "" },
  }).then((res) => {
    if (res.ok) {
      location.replace("/auth");
    }
  });
});
