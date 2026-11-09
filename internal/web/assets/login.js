// One-use link exchange (GAP-SEC-07). The nonce travels in the URL fragment,
// so it is never sent to the server, logged, or kept in the history entry.
const status = document.querySelector("#status");

function fail(text) {
  status.textContent = text;
}

const nonce = decodeURIComponent(location.hash.slice(1));
history.replaceState(null, "", "/auth");

if (!nonce) {
  fail("Open the one-time link printed by `damnbot serve open`.");
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
        fail("This link was already used or has expired.");
      }
    })
    .catch(() => fail("The local server did not answer."));
}
