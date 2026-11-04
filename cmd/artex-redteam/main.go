// Command artex-redteam runs the ARTEX-RedTeam backend: the PostgreSQL-backed asset and
// exploration graphs, the event-driven exploration engine, and the JSON HTTP
// API consumed by the shadcn/ui frontend (embedded with -tags embedui).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/agent"
	"github.com/Bluestar-coder/ARTEX-RedTeam/config"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/volumecheck"
	"github.com/Bluestar-coder/ARTEX-RedTeam/selfupdate"
	"github.com/Bluestar-coder/ARTEX-RedTeam/server"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=<tag>". Defaults to "dev" for local builds.
var version = "dev"

const banner = `
    _    ____ _____ _______  __
   / \  |  _ \_   _| ____\ \/ /
  / _ \ | |_) || | |  _|  \  /
 / ___ \|  _ < | | | |___ /  \
/_/   \_\_| \_\|_| |_____/_/\_\
`

// printBanner writes the startup banner + version/runtime info to stdout.
func printBanner(addr string) {
	fmt.Print(banner)
	fmt.Println("  AI 自主红队作战系统")
	fmt.Printf("  版本 %s  ·  %s/%s  ·  %s  ·  监听 %s\n\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), addr)
}

// main only maps run's result onto the process exit code. The exit code is part
// of the update protocol — the supervising start script reads it to decide
// whether to relaunch us (see selfupdate.ExitRestart) — so the body has to live
// in a function that can *return* rather than os.Exit past its own defers.
func main() {
	os.Exit(run())
}

func run() int {
	var (
		addr    = flag.String("addr", "127.0.0.1:8787", "HTTP listen address (default loopback; use :8787 to expose on all interfaces)")
		dataDir = flag.String("data", config.DefaultDataDir(), "data directory for the traffic index, JWT key and workspaces (default: data-redteam/ next to the executable)")
		proxy   = flag.String("proxy", "127.0.0.1:8788", "traffic recording proxy address (empty to disable)")
		proxyGW = flag.String("proxy-pool-gateway", "127.0.0.1:8789", "proxy-pool forwarding gateway address (empty to disable 入口C)")
		// 一次性卷检测模式：由 compose 的 volume-check 容器调用，只读 docker.sock
		// 检测“换目录后连到新空库、旧 pgdata 卷还在”的陷阱，把提示写进数据目录后
		// 退出。正常启动不会走到这里。
		volumeCheck = flag.Bool("volume-check", false, "internal: detect compose volume migration hints and exit")
	)
	flag.Parse()

	if *volumeCheck {
		return volumecheck.RunSidecar(*dataDir)
	}

	// 默认只监听回环：管理面等价于 root（能配 MCP、能造 bash 工具），不该
	// 在没设防火墙的机器上默认暴露。确需对外时显式传 -addr :8787。
	if host, _, err := net.SplitHostPort(*addr); err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		log.Printf("[warn] 管理端监听在非回环地址 %s：请确保前面有 VPN、SSH 隧道或带认证的反向代理", *addr)
	}

	// Data directory: adopt a pre-rename data/ directory next to the binary so an
	// in-place upgrade keeps jwt.key, the traffic index and workspaces. Only runs
	// when the user did not pass an explicit -data.
	if *dataDir == config.DefaultDataDir() {
		actual, legacy := config.AdoptLegacyDataDir(*dataDir)
		switch {
		case legacy != "":
			log.Printf("[config] 数据目录已从 %s 迁移到 %s", legacy, actual)
		case actual != *dataDir:
			log.Printf("[config] 数据目录迁移失败，继续使用旧目录 %s（建议手动迁移到 %s）", actual, *dataDir)
		}
		*dataDir = actual
	}

	// hand the build version to the server package so GET /api/health can report it
	// to the frontend top bar.
	server.BuildVersion = version

	printBanner(*addr)

	// capture backend logs into the in-memory sink (still to stderr) so the /logs
	// page can show a live log stream. Do this first, to catch startup logs too.
	server.StartLogCapture()

	// Self-update bootstrap: swap in a staged binary, or count a post-swap boot
	// attempt and roll back if the new build keeps dying. Must run before we open
	// the stores or bind a port — this may end with "exit and let the start script
	// relaunch me", and there is no point paying for either first.
	action, upState := selfupdate.Bootstrap()
	server.SetBootUpdateState(upState)
	if action == selfupdate.Restart {
		return selfupdate.ExitRestart
	}

	// surface which config file the binary reads (absolute, so `go run`'s relative
	// "config.json" — resolved against the CWD — is unambiguous).
	cfgPath := config.Path()
	if abs, e := filepath.Abs(cfgPath); e == nil {
		cfgPath = abs
	}
	if _, e := os.Stat(cfgPath); e == nil {
		log.Printf("[config] 配置文件: %s", cfgPath)
	} else {
		log.Printf("[config] 配置文件: %s (不存在 — 将仅尝试环境变量 ARTEX_REDTEAM_PG_DSN/ARTEX_PG_DSN)", cfgPath)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := shutdownContext(sigCtx)
	defer shutdown(agent.AbortShutdown)

	mgr, err := server.NewManager(*dataDir, *proxy, *proxyGW)
	if err != nil {
		log.Fatalf("open stores: %v", err)
	}
	defer mgr.Close()

	// Surviving this long means a freshly swapped-in build actually works, so drop
	// the upgrade marker and stop counting attempts. Until it fires, every boot
	// increments the count and a build that keeps dying gets rolled back.
	settle := time.AfterFunc(selfupdate.SettleDelay, selfupdate.Settle)
	defer settle.Stop()

	skillDir := config.SkillDir()
	if abs, err := filepath.Abs(skillDir); err == nil {
		skillDir = abs
	}
	log.Printf("[config] skill 目录: %s", skillDir)
	srv := server.New(ctx, mgr, skillDir, *dataDir, config.BaseDir())
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("ARTEX-RedTeam %s backend listening on %s (data=%s, workers=%d)", version, *addr, *dataDir, mgr.Workers())
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Two ways out: a signal (normal stop → exit 0, the start script stops looping)
	// or a staged update / rollback (→ exit 75, the script relaunches us and the
	// bootstrap above installs the new build).
	code := 0
	select {
	case <-ctx.Done():
	case <-server.RestartRequested():
		code = selfupdate.ExitRestart
		shutdown(agent.AbortShutdown)
	}

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	return code
}

// shutdownContext deliberately does not derive from signalCtx. If it did, the
// parent's plain context.Canceled could win the race before AbortShutdown was
// attached to the child, losing the diagnostic cause in every running Agent.
func shutdownContext(signalCtx context.Context) (context.Context, context.CancelCauseFunc) {
	ctx, shutdown := context.WithCancelCause(context.Background())
	go func() {
		select {
		case <-signalCtx.Done():
			shutdown(agent.AbortShutdown)
		case <-ctx.Done():
		}
	}()
	return ctx, shutdown
}
