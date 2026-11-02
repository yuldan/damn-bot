//go:build !minimal

package main

import (
	"fmt"
	"io"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec/code"
)

// runStarlark is the hidden child interpreter used by execute_code language
// "starlark". It only runs one file from the per-call temp dir and exposes the
// tool(name, **args) binding over the private parent socket (GAP-COST-10).
func runStarlark(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		writeLine(stderr, "__starlark: one script path expected")
		return 2
	}
	sock := config.OS().Getenv("DAMNBOT_TOOL_SOCKET")
	pre := starlark.StringDict{"tool": starlark.NewBuiltin("tool", func(_ *starlark.Thread, _ *starlark.Builtin, vals starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(vals) != 1 {
			return nil, fmt.Errorf("tool(name, **args): one name expected")
		}
		name, ok := starlark.AsString(vals[0])
		if !ok || name == "" {
			return nil, fmt.Errorf("tool(name, **args): name must be a string")
		}
		call := make(map[string]any, len(kwargs))
		for _, kw := range kwargs {
			if len(kw) != 2 {
				return nil, fmt.Errorf("tool: malformed keyword")
			}
			key, _ := starlark.AsString(kw[0])
			val, cerr := starlarkValue(kw[1])
			if cerr != nil {
				return nil, cerr
			}
			call[key] = val
		}
		if sock == "" {
			return nil, fmt.Errorf("denied: tool RPC is unavailable")
		}
		res, err := code.CallToolRPC(sock, name, call)
		if err != nil {
			return nil, fmt.Errorf("tool: %w", err)
		}
		return starlark.String(string(res)), nil
	})}
	thread := &starlark.Thread{Name: "execute_code"}
	thread.Print = func(_ *starlark.Thread, msg string) { writeLine(stdout, msg) }
	if _, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, args[0], nil, pre); err != nil {
		writeLine(stderr, cleanLine(err.Error()))
		return 1
	}
	return 0
}

// starlarkValue converts the JSON-shaped Starlark subset to Go values.
func starlarkValue(v starlark.Value) (any, error) {
	switch x := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(x), nil
	case starlark.String:
		return string(x), nil
	case starlark.Int:
		n, ok := x.Int64()
		if !ok {
			return nil, fmt.Errorf("tool: integer out of range")
		}
		return n, nil
	case starlark.Float:
		return float64(x), nil
	case *starlark.List:
		return starlarkList(x)
	case starlark.Tuple:
		return starlarkTuple(x)
	case *starlark.Dict:
		return starlarkDict(x)
	default:
		return v.String(), nil
	}
}

func starlarkList(x *starlark.List) (any, error) {
	out := make([]any, 0, x.Len())
	for i := range x.Len() {
		item, err := starlarkValue(x.Index(i))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func starlarkTuple(x starlark.Tuple) (any, error) {
	out := make([]any, 0, len(x))
	for _, item := range x {
		g, err := starlarkValue(item)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func starlarkDict(x *starlark.Dict) (any, error) {
	out := make(map[string]any, x.Len())
	for _, item := range x.Items() {
		key, _ := starlark.AsString(item[0])
		g, err := starlarkValue(item[1])
		if err != nil {
			return nil, err
		}
		out[key] = g
	}
	return out, nil
}

func writeLine(w io.Writer, s string) {
	if _, err := io.WriteString(w, s+"\n"); err != nil {
		return
	}
}
