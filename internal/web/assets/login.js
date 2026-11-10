// One-use link exchange (GAP-SEC-07). The nonce travels in the URL fragment,
// so it is never sent to the server, logged, or kept in the history entry.
// Messages come from the catalog via data-msg-* attributes (GAP-INF-08).
const status = document.querySelector("#status");
const msg = document.body.dataset;

function fail(text) {
  status.textContent = text;
}

const nonce = decodeURIComponent(location.hash.slice(1));
history.replaceState(null, "", "/auth");

if (!nonce) {
  fail(msg.msgNoNonce ?? "");
} else {
  fetch("/auth/exchange", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ nonce }),
  })
    .then((res) => {
      if (res.ok) {
        location.replace("/");
      } else {
        fail(msg.msgUsed ?? "");
      }
    })
    .catch(() => fail(msg.msgUnreachable ?? ""));
}
