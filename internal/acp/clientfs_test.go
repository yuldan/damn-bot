package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type recordedCall struct {
	method string
	params map[string]any
}

type fakeRequester struct {
	calls []recordedCall
	reply func(method string) (any, error)
}

func (f *fakeRequester) Request(_ context.Context, method string, params, result any) error {
	p, ok := params.(map[string]any)
	if !ok {
		p = map[string]any{}
	}
	f.calls = append(f.calls, recordedCall{method: method, params: p})
	if f.reply == nil {
		return nil
	}
	v, err := f.reply(method)
	if err != nil {
		return err
	}
	if result == nil || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("fake reply: %w", err)
	}
	if err := json.Unmarshal(b, result); err != nil {
		return fmt.Errorf("fake reply: %w", err)
	}
	return nil
}

func TestClientFSReadParamsAndContent(t *testing.T) {
	r := &fakeRequester{reply: func(string) (any, error) {
		return map[string]string{"content": "buffer\n"}, nil
	}}
	c := &ClientFS{SessionID: "s1", Read: true, Write: true, Requester: r}
	data, notFound, err := c.ReadTextFile(t.Context(), "/work/a.txt")
	if err != nil || notFound || string(data) != "buffer\n" {
		t.Fatalf("ReadTextFile = %q, %v, %v", data, notFound, err)
	}
	if len(r.calls) != 1 || r.calls[0].method != "fs/read_text_file" {
		t.Fatalf("calls = %+v", r.calls)
	}
	if r.calls[0].params["sessionId"] != "s1" || r.calls[0].params["path"] != "/work/a.txt" {
		t.Fatalf("params = %+v", r.calls[0].params)
	}
}

func TestClientFSWriteParams(t *testing.T) {
	r := &fakeRequester{}
	c := &ClientFS{SessionID: "s1", Read: true, Write: true, Requester: r}
	if err := c.WriteTextFile(t.Context(), "/work/a.txt", []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0].method != "fs/write_text_file" {
		t.Fatalf("calls = %+v", r.calls)
	}
	if r.calls[0].params["content"] != "new\n" || r.calls[0].params["path"] != "/work/a.txt" {
		t.Fatalf("params = %+v", r.calls[0].params)
	}
}

func TestClientFSCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name        string
		read, write bool
		wantRead    bool
		wantWrite   bool
	}{
		{"none", false, false, false, false},
		{"read", true, false, true, false},
		{"write", false, true, false, false},
		{"both", true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ClientFS{Read: tc.read, Write: tc.write, Requester: &fakeRequester{}}
			if got := c.Supports(false); got != tc.wantRead {
				t.Fatalf("Supports(read) = %v", got)
			}
			if got := c.Supports(true); got != tc.wantWrite {
				t.Fatalf("Supports(write) = %v", got)
			}
		})
	}
}

func TestClientFSNotFoundAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"error_code", &RemoteError{Code: -32602, Message: "no", Data: json.RawMessage(`{"error_code":"not_found"}`)}, true},
		{"resource_code", &RemoteError{Code: -32002, Message: "no"}, true},
		{"other", &RemoteError{Code: -32603, Message: "boom"}, false},
		{"plain", errors.New("tcp reset"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ClientFS{Read: true, Requester: &fakeRequester{reply: func(string) (any, error) { return nil, tc.err }}}
			_, notFound, err := c.ReadTextFile(t.Context(), "/x")
			if notFound != tc.want {
				t.Fatalf("notFound = %v, want %v", notFound, tc.want)
			}
			if !tc.want && err == nil {
				t.Fatal("error was swallowed")
			}
		})
	}
}

func TestClientFSBufferCap(t *testing.T) {
	r := &fakeRequester{reply: func(string) (any, error) {
		return map[string]string{"content": strings.Repeat("a", maxClientFSBytes+1)}, nil
	}}
	c := &ClientFS{Read: true, Requester: r}
	if _, _, err := c.ReadTextFile(t.Context(), "/x"); err == nil {
		t.Fatal("oversized buffer accepted")
	}
}
