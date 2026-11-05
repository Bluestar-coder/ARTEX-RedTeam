package toolbuild

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/norma/permission"
	actool "github.com/Bluestar-coder/ARTEX-RedTeam/norma/tool"
)

func noopRun(context.Context, json.RawMessage) (actool.Result, error) {
	return actool.Text("ok"), nil
}

func validSpec() Spec {
	return Spec{
		Name:        "demo",
		Description: "demo tool",
		Schema:      Object(map[string]any{"x": String("x")}, "x"),
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Text("ok"), nil
		},
		Access:     AccessReadOnly,
		Permission: PermissionAllow,
	}
}

func TestBuildRequiresEveryDeclaration(t *testing.T) {
	cases := map[string]func(*Spec){
		"missing name":       func(s *Spec) { s.Name = "" },
		"missing schema":     func(s *Spec) { s.Schema = nil },
		"missing run":        func(s *Spec) { s.Run = nil },
		"missing access":     func(s *Spec) { s.Access = AccessUnset },
		"missing permission": func(s *Spec) { s.Permission = PermissionUnset },
		"negative timeout":   func(s *Spec) { s.Timeout = -time.Second },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := validSpec()
			mutate(&s)
			defer func() {
				if recover() == nil {
					t.Fatalf("Build(%s) did not panic", name)
				}
			}()
			Build(s)
		})
	}
}

func TestReadAndWriteKeepOldSemantics(t *testing.T) {
	read := Read("r", "read tool", Object(nil), noopRun)
	if read.Name() != "r" || read.Description() != "read tool" {
		t.Fatalf("read identity: %q %q", read.Name(), read.Description())
	}
	if !read.IsReadOnly(nil) || !read.IsConcurrencySafe(nil) {
		t.Fatal("read tool must be read-only and concurrency-safe")
	}
	if got := read.CheckPermissions(context.Background(), nil, permission.Context{}); !reflect.DeepEqual(got, permission.Allowed()) {
		t.Fatalf("read permission = %+v", got)
	}
	if read.InputSchema() == nil {
		t.Fatal("read schema must be kept")
	}

	write := Write("w", "write tool", Object(nil), noopRun)
	if write.IsReadOnly(nil) || write.IsConcurrencySafe(nil) {
		t.Fatal("write tool must be neither read-only nor concurrency-safe")
	}
	if got := write.CheckPermissions(context.Background(), nil, permission.Context{}); !reflect.DeepEqual(got, permission.Allowed()) {
		t.Fatalf("write permission = %+v", got)
	}
}

func TestAskPermissionCarriesPrompt(t *testing.T) {
	tool := Build(Spec{
		Name: "mcp__srv__do", Description: "d", Schema: Object(nil),
		Access: AccessReadWrite, Permission: PermissionAsk, AskPrompt: "call MCP tool mcp__srv__do?",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Text("ok"), nil
		},
	})
	got := tool.CheckPermissions(context.Background(), nil, permission.Context{})
	want := permission.AskUser("call MCP tool mcp__srv__do?")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ask decision = %+v, want %+v", got, want)
	}
}

func TestTimeoutWrapsRun(t *testing.T) {
	tool := Build(Spec{
		Name: "slow", Description: "slow", Schema: Object(nil),
		Access: AccessReadOnly, Permission: PermissionAllow, Timeout: 20 * time.Millisecond,
		Run: func(ctx context.Context, _ json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			<-ctx.Done()
			return actool.Errorf(ctx.Err().Error()), nil
		},
	})
	res, err := tool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Call returned transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "deadline") {
		t.Fatalf("timeout result = %+v", res)
	}
}

func TestSchemaHelpersMatchLegacyJSON(t *testing.T) {
	cases := []struct {
		name string
		got  map[string]any
		want string
	}{
		{"object", Object(map[string]any{"a": String("x")}, "a"), `{"properties":{"a":{"description":"x","type":"string"}},"required":["a"],"type":"object"}`},
		{"object no required", Object(map[string]any{}), `{"properties":{},"type":"object"}`},
		{"string", String("x"), `{"description":"x","type":"string"}`},
		{"string empty desc", String(""), `{"type":"string"}`},
		{"integer", Integer("n"), `{"description":"n","type":"integer"}`},
		{"boolean", Boolean("b"), `{"description":"b","type":"boolean"}`},
		{"array", Array(String(""), "items"), `{"description":"items","items":{"type":"string"},"type":"array"}`},
		{"enum", EnumString("", "a", "b"), `{"enum":["a","b"],"type":"string"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.got)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Fatalf("%s: got %s, want %s", c.name, b, c.want)
		}
	}
}

func TestEnumStringRejectsEmptyValues(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("EnumString with no values did not panic")
		}
	}()
	EnumString("d")
}
