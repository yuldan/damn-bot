// Settings: preview a `[ui]` change, show the diff, then apply the exact
// previewed patch with its one-time token. No framework, no innerHTML.
const csrf = document.querySelector('meta[name="damnbot-csrf"]')?.content;
const form = document.querySelector("#settings-form");
const select = document.querySelector("#language-select");
const diff = document.querySelector("#diff");
const apply = document.querySelector("#apply");
const status = document.querySelector("#settings-status");
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
      status.textContent = "Preview refused.";
      return;
    }
    const data = await res.json();
    token = data.token;
    diff.textContent = data.diff;
    diff.hidden = false;
    apply.hidden = false;
    status.textContent = "Review the diff, then apply.";
  });
});

apply?.addEventListener("click", () => {
  post("/api/settings/apply", { token }).then((res) => {
    if (res.ok) {
      status.textContent = "Applied. Reload to see the new language.";
      apply.hidden = true;
      return;
    }
    status.textContent = "Preview expired or the write failed.";
  });
});
