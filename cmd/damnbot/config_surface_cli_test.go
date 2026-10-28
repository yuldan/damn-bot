package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules"
)

func TestConfigModulesListsCompiledRegistry(t *testing.T) {
	t.Parallel()
	code, out, errOut := runArgs(t, "config", "modules")
	if code != 0 {
		t.Fatalf("config modules = (%d, %s)", code, errOut)
	}
	if !strings.HasPrefix(out, "modules: 0 compiled in\n") {
		t.Fatalf("config modules stdout = %q, want the compiled-in count first", out)
	}
	if !strings.Contains(out, `[modules.<id>] is rejected as "module section unavailable"`) {
		t.Errorf("config modules lacks the F3 config note:\n%s", out)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want empty", errOut)
	}
	if strings.Contains(out, "id=") {
		t.Errorf("empty registry listed a module:\n%s", out)
	}
}

func TestWriteModuleListDescriptorShape(t *testing.T) {
	t.Parallel()
	descs := []modules.Descriptor{
		{ID: "alpha", Group: "code", Tools: []modules.ToolDescriptor{{Name: "a"}, {Name: "b"}}},
		{ID: "beta", Group: "browser", ConfigKey: "modules.beta-custom"},
	}
	var out bytes.Buffer
	if code := writeModuleList(&out, descs); code != 0 {
		t.Fatalf("writeModuleList = %d", code)
	}
	want := "modules: 2 compiled in\n" +
		"  id=alpha group=code config=modules.alpha tools=2\n" +
		"  id=beta group=browser config=modules.beta-custom tools=0\n" +
		"note: the module engine is not wired in this build (F4/F5); [modules.<id>] is rejected as \"module section unavailable\" until then\n"
	if out.String() != want {
		t.Fatalf("writeModuleList =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestConfigModulesRestartUnavailable(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"config", "modules", "restart", "browser"},
		{"config", "modules", "restart", "no-such-module"},
	}
	for _, args := range cases {
		code, out, errOut := runArgs(t, args...)
		if code != 2 {
			t.Errorf("%q exit = %d, want 2", args, code)
		}
		if out != "" {
			t.Errorf("%q stdout = %q, want empty", args, out)
		}
		if !strings.Contains(errOut, "was not restarted") || !strings.Contains(errOut, "F4/F5") {
			t.Errorf("%q stderr lacks the unavailable reason: %s", args, errOut)
		}
	}
	code, _, errOut := runArgs(t, "config", "modules", "restart")
	if code != 2 || !strings.Contains(errOut, "one module id expected") {
		t.Errorf("bare restart = (%d, %s), want exit 2 naming the missing id", code, errOut)
	}
}

func TestConfigModulesUnknownSubcommand(t *testing.T) {
	t.Parallel()
	code, out, errOut := runArgs(t, "config", "modules", "list")
	if code != 2 || out != "" || !strings.Contains(errOut, `unknown subcommand "list"`) {
		t.Fatalf("config modules list = (%d, %q, %q), want exit 2 with the usage", code, out, errOut)
	}
}

func TestConfigHooksUnavailable(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"config", "hooks", "ls"},
		{"config", "hooks", "test", "pre_tool"},
	}
	for _, args := range cases {
		code, out, errOut := runArgs(t, args...)
		if code != 2 {
			t.Errorf("%q exit = %d, want 2", args, code)
		}
		if out != "" {
			t.Errorf("%q stdout = %q, want empty", args, out)
		}
		if !strings.Contains(errOut, "hook engine") || !strings.Contains(errOut, "F4") {
			t.Errorf("%q stderr lacks the F4 reason: %s", args, errOut)
		}
	}
	code, _, errOut := runArgs(t, "config", "hooks", "test")
	if code != 2 || !strings.Contains(errOut, "usage: damnbot config hooks ls|test <event>") {
		t.Errorf("bare hooks test = (%d, %s), want exit 2 with usage", code, errOut)
	}
}
