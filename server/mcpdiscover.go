package server

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/config"
	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/mcphttp"
	"github.com/Bluestar-coder/ARTEX-RedTeam/norma/mcp"
	actool "github.com/Bluestar-coder/ARTEX-RedTeam/norma/tool"
)

// mcpClient is the shared surface of a connected MCP server (stdio, Streamable HTTP,
// or legacy SSE),
// so tools/list and cleanup are handled uniformly regardless of transport.
type mcpClient interface {
	Tools(context.Context) ([]actool.CoreTool, error)
	Close() error
}

// connectMCP dials one MCP server per its transport. Callers must Close the client.
func connectMCP(ctx context.Context, m *db.MCPServer) (mcpClient, error) {
	switch m.Transport {
	case "stdio":
		if m.Command == "" {
			return nil, fmt.Errorf("stdio 传输缺少命令")
		}
		if err := validateStdioCommand(m.Command, jsonStrSlice(m.Args)); err != nil {
			return nil, err
		}
		return mcp.NewStdioClient(ctx, m.Name, m.Command, jsonStrMap(m.Env), jsonStrSlice(m.Args)...)
	case "http":
		if m.URL == "" {
			return nil, fmt.Errorf("http 传输缺少 URL")
		}
		// env map doubles as HTTP headers (e.g. Authorization).
		return mcphttp.New(ctx, m.Name, m.URL, jsonStrMap(m.Env), m.Insecure)
	case "sse":
		if m.URL == "" {
			return nil, fmt.Errorf("sse 传输缺少 URL")
		}
		return mcphttp.NewSSE(ctx, m.Name, m.URL, jsonStrMap(m.Env), m.Insecure)
	default:
		return nil, fmt.Errorf("未知传输方式 %q", m.Transport)
	}
}

// shellInterpreters 是「只用来执行内联命令」的解释器。把这类程序配成 stdio MCP
// 服务器没有正当用途，却是把「配置一条 MCP」变成 webshell 的标准做法。
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true,
	"busybox": true, "cmd": true, "cmd.exe": true,
	"powershell": true, "powershell.exe": true, "pwsh": true, "pwsh.exe": true,
}

// inlineCodeInterpreters 正常可以作为 MCP 服务器（python server.py / node
// server.js），只有带 -c / -e 的内联代码形态才和 shell 一样危险。
var inlineCodeInterpreters = map[string]bool{
	"python": true, "python3": true, "perl": true, "ruby": true, "node": true, "php": true,
}

var inlineCodeFlags = map[string]bool{
	"-c": true, "-e": true, "-r": true, "--eval": true, "-command": true, "-encodedcommand": true,
	"/c": true, "/k": true,
}

// stdioWrappers are launchers whose only job is to run another program. "env sh
// -c …" and "nohup bash -c …" are the obvious ways around a naive basename
// check, so peel them off (bounded depth) before classifying the real command.
var stdioWrappers = map[string]bool{
	"env": true, "nohup": true, "setsid": true, "command": true, "exec": true, "time": true,
}

// unwrapStdioCommand strips launcher wrappers and their options, returning the
// underlying command and arguments. Bounded to avoid pathological nesting.
func unwrapStdioCommand(command string, args []string) (string, []string) {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	for depth := 0; depth < 4 && stdioWrappers[base]; depth++ {
		i := 0
		for i < len(args) {
			a := args[i]
			if a == "--" {
				i++
				break
			}
			if strings.HasPrefix(a, "-") || (base == "env" && strings.Contains(a, "=")) {
				// options that take a separate value
				if (a == "-u" || a == "--unset" || a == "-P" || a == "--chdir") && i+1 < len(args) {
					i += 2
					continue
				}
				i++
				continue
			}
			break
		}
		if i >= len(args) {
			return base, nil
		}
		base = strings.ToLower(filepath.Base(strings.TrimSpace(args[i])))
		args = args[i+1:]
	}
	return base, args
}

// validateStdioCommand rejects the webshell shape "sh -c <payload>" for MCP stdio
// servers. The transport is a legitimate local-exec feature (it is how npx /
// uvx MCP servers start), so the check targets interpreters with inline code
// rather than blocking subprocesses outright. Operators who genuinely need a
// shell-backed MCP server can set ARTEX_REDTEAM_ALLOW_STDIO_SHELL=1.
func validateStdioCommand(command string, args []string) error {
	if config.Env("ALLOW_STDIO_SHELL") == "1" {
		return nil
	}
	base, args := unwrapStdioCommand(command, args)
	if shellInterpreters[base] {
		return fmt.Errorf("stdio MCP 拒绝把 shell 解释器（%s）作为启动命令：这是 webshell 形态而非 MCP 服务器。如确需，设置 ARTEX_REDTEAM_ALLOW_STDIO_SHELL=1", base)
	}
	if inlineCodeInterpreters[base] {
		for _, a := range args {
			if inlineCodeFlags[strings.ToLower(strings.TrimSpace(a))] {
				return fmt.Errorf("stdio MCP 拒绝内联代码参数（%s %s）：请改为启动具体的 MCP 服务器脚本。如确需，设置 ARTEX_REDTEAM_ALLOW_STDIO_SHELL=1", base, a)
			}
		}
	}
	return nil
}

// discoverAndCacheMCP connects to one MCP, lists its tools, and persists the tool
// names to mcp_tools_cache so the UI shows them without a live connection.
func (s *Server) discoverAndCacheMCP(ctx context.Context, m *db.MCPServer) error {
	cl, err := connectMCP(ctx, m)
	if err != nil {
		return err
	}
	defer cl.Close()
	ts, err := cl.Tools(ctx)
	if err != nil {
		return err
	}
	tools := make([]db.MCPTool, 0, len(ts))
	for _, t := range ts {
		tools = append(tools, db.MCPTool{Name: t.Name(), Description: t.Description()})
	}
	if err := s.m.pg.SaveMCPTools(m.ID, tools); err != nil {
		return err
	}
	log.Printf("[mcp] %s 发现 %d 个工具并已缓存", m.Name, len(tools))
	return nil
}

// discoverEmptyMCPsOnStartup fills the tool cache for any enabled MCP that has none
// yet (notably the seeded browser MCP on first run). Runs sequentially in one
// goroutine so we never spawn many stdio servers (npx) at once, and never blocks
// startup. Best-effort: a failure leaves the cache empty to retry next start.
func (s *Server) discoverEmptyMCPsOnStartup() {
	servers, err := s.m.pg.ListMCP()
	if err != nil {
		log.Printf("[mcp] 启动自动发现: 读取列表失败: %v", err)
		return
	}
	for _, m := range servers {
		if !m.Enabled || len(m.Tools) > 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
		if err := s.discoverAndCacheMCP(ctx, m); err != nil {
			log.Printf("[mcp] 启动自动发现 %s 失败: %v", m.Name, err)
		}
		cancel()
	}
}
