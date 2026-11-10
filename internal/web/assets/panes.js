// Terminal and preview panes plus named projects (UI-017). The desktop shell
// is replaced by this page: no bundled runtime. All copy arrives from
// data-msg-* attributes; every element is built with textContent.
const csrf = document.querySelector('meta[name="damnbot-csrf"]')?.content;
const msg = document.body.dataset;
const space = "\u0020";
const kind = document.querySelector("#pane-kind");
const target = document.querySelector("#pane-target");
const openForm = document.querySelector("#pane-open");
const paneList = document.querySelector("#pane-list");
const output = document.querySelector("#pane-output");
const projectList = document.querySelector("#project-list");
let selected = "";

function post(path, body) {
  return fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf ?? "" },
    body: JSON.stringify(body),
  });
}

function row(text, buttonText, onClick) {
  const li = document.createElement("li");
  const label = document.createElement("span");
  label.textContent = text;
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = buttonText;
  button.addEventListener("click", onClick);
  li.append(label, button);
  return li;
}

async function refreshPanes() {
  const res = await fetch("/api/panes", { headers: { Accept: "application/json" } });
  if (!res.ok) return;
  const data = await res.json();
  paneList.replaceChildren();
  for (const pane of data.panes) {
    paneList.append(row(`${pane.kind} ${pane.title}`, msg.msgPaneClose ?? "", () => {
      post("/api/panes/close", { id: pane.id }).then(() => {
        if (selected === pane.id) {
          selected = "";
          output.hidden = true;
        }
        refreshPanes();
      });
    }));
    paneList.lastElementChild?.prepend(document.createTextNode(""));
  }
  if (!selected && data.panes.length > 0) {
    selected = data.panes[0].id;
    readOutput();
  }
}

async function readOutput() {
  if (!selected) return;
  const res = await fetch(`/api/panes/output?id=${encodeURIComponent(selected)}&offset=0`, {
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    output.textContent = msg.msgPaneRefused ?? "";
    output.hidden = false;
    return;
  }
  const data = await res.json();
  output.textContent = new TextDecoder().decode(new Uint8Array(data.data ?? []));
  output.hidden = false;
}

async function refreshProjects() {
  const res = await fetch("/api/projects", { headers: { Accept: "application/json" } });
  if (!res.ok) return;
  const data = await res.json();
  projectList.replaceChildren();
  for (const project of data.projects) {
    const mark = project.current ? "*" + space : space + space;
    projectList.append(row(`${mark}${project.name} (${project.folders.length})`, msg.msgPaneOpen ?? "", () => {
      target.value = project.primary ?? "";
    }));
  }
}

openForm?.addEventListener("submit", (event) => {
  event.preventDefault();
  post("/api/panes/open", { kind: kind.value, target: target.value }).then(async (res) => {
    if (!res.ok) {
      output.textContent = msg.msgPaneRefused ?? "";
      output.hidden = false;
      return;
    }
    const pane = await res.json();
    selected = pane.id;
    await refreshPanes();
    await readOutput();
  });
});

output?.addEventListener("click", () => {
  readOutput();
});

refreshPanes();
refreshProjects();
