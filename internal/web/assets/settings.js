// Settings: preview a `[ui]` change, show the diff, then apply the exact
// previewed patch with its one-time token. No framework; textContent only.
// Messages come from the catalog via data-msg-* attributes (GAP-INF-08).
const csrf = document.querySelector('meta[name="damnbot-csrf"]')?.content;
const form = document.querySelector("#settings-form");
const select = document.querySelector("#language-select");
const diff = document.querySelector("#diff");
const apply = document.querySelector("#apply");
const status = document.querySelector("#settings-status");
const msg = document.body.dataset;
let token = "";

function post(path, body) {
  return fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf ?? "" },
    body: JSON.stringify(body),
  });
}

form?.addEventListener("submit", (event) => {
  event.preventDefault();
  post("/api/settings/preview", { language: select.value }).then(async (res) => {
    if (!res.ok) {
      status.textContent = msg.msgPreviewRefused ?? "";
      return;
    }
    const data = await res.json();
    token = data.token;
    diff.textContent = data.diff;
    diff.hidden = false;
    apply.hidden = false;
    status.textContent = msg.msgReview ?? "";
  });
});

apply?.addEventListener("click", () => {
  post("/api/settings/apply", { token }).then((res) => {
    if (res.ok) {
      status.textContent = msg.msgApplied ?? "";
      apply.hidden = true;
      return;
    }
    status.textContent = msg.msgExpired ?? "";
  });
});
