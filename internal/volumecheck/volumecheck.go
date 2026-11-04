// Package volumecheck detects the "empty database after switching compose
// project directories" trap.
//
// Compose derives the project name from the directory name, and the named volume
// `pgdata` is per project. A user who clones/renames the repo to a new directory
// and runs `docker compose up` therefore gets a brand-new empty volume — the old
// data is still on the machine under the old project's volume, but nothing
// points at it anymore and the app silently starts with an empty database.
//
// Detection runs in a short-lived sidecar container (the `volume-check` compose
// service) that has the Docker socket mounted read-only. It writes a hint file
// into the shared data directory; the main process reads it at startup and only
// warns when its own database is still uninitialised. Nothing is migrated
// automatically — the hint only names the old volume/directory and prints the
// commands needed to reconnect.
package volumecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HintFile is the name of the state file written next to the data directory.
const HintFile = ".compose-volume-warning.json"

// DoneFile marks that the sidecar ran, so the supervising script can wait for it
// without blocking startup for long.
const DoneFile = ".volume-check.done"

// SocketEnv overrides the Docker socket path (tests and unusual installs).
const SocketEnv = "ARTEX_REDTEAM_DOCKER_SOCK"

// Project describes a compose project that may still own old data.
type Project struct {
	// Name is the compose project name (by default the directory name).
	Name string `json:"name"`
	// Volume is the detected `<project>_pgdata` volume, if any.
	Volume string `json:"volume,omitempty"`
	// DataDir is the host path of the project's bind-mounted `./data`, when a
	// stopped/running container of that project still records it.
	DataDir string `json:"data_dir,omitempty"`
}

// Hint is the payload written by the sidecar and consumed by the app.
type Hint struct {
	CurrentProject string    `json:"current_project"`
	OldProjects    []Project `json:"old_projects"`
}

// ReadHint loads the hint file. A missing file means "no sidecar ran" and is not
// an error for the caller.
func ReadHint(dataDir string) (*Hint, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, HintFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var h Hint
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// WriteHint persists the hint atomically, then leaves the done marker.
func WriteHint(dataDir string, h *Hint) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(dataDir, HintFile)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return touchDone(dataDir)
}

// ClearHint removes a stale hint (the machine no longer has old volumes) and
// still leaves the done marker.
func ClearHint(dataDir string) error {
	if err := os.Remove(filepath.Join(dataDir, HintFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return touchDone(dataDir)
}

func touchDone(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, DoneFile), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(time.Now().Format(time.RFC3339))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// RunSidecar is the `-volume-check` entry point. It must never make the boot
// fail: every error is printed and turned into exit code 0.
func RunSidecar(dataDir string) int {
	client, err := dockerClient()
	if err != nil {
		log.Printf("[volume-check] 跳过检测：%v（非 Docker 部署或未挂载 docker.sock 属正常）", err)
		_ = touchDone(dataDir)
		return 0
	}
	hint, err := Detect(context.Background(), client)
	if err != nil {
		log.Printf("[volume-check] 跳过检测：%v", err)
		_ = touchDone(dataDir)
		return 0
	}
	if len(hint.OldProjects) == 0 {
		if err := ClearHint(dataDir); err != nil {
			log.Printf("[volume-check] 清理旧提示失败：%v", err)
		}
		log.Printf("[volume-check] 未发现其它 compose 项目的数据卷（当前项目 %s），不提示", hint.CurrentProject)
		return 0
	}
	if err := WriteHint(dataDir, hint); err != nil {
		log.Printf("[volume-check] 写入提示失败：%v", err)
		return 0
	}
	for _, line := range LogLines(hint) {
		log.Print(line)
	}
	return 0
}

// --- Docker API access -------------------------------------------------------

type dockerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	Mounts []dockerMount     `json:"Mounts"`
}

type dockerVolume struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

func dockerClient() (*http.Client, error) {
	sock := os.Getenv(SocketEnv)
	if sock == "" {
		sock = "/var/run/docker.sock"
	}
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("docker socket %s 不可用: %w", sock, err)
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", sock)
		},
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}, nil
}

func getJSON(ctx context.Context, c *http.Client, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Detect queries the Docker API and returns the hint for the current project.
// It is exported so tests can drive it with a fake socket.
func Detect(ctx context.Context, c *http.Client) (*Hint, error) {
	var containers []dockerContainer
	if err := getJSON(ctx, c, "/containers/json?all=1", &containers); err != nil {
		return nil, err
	}
	var volumes struct {
		Volumes []dockerVolume `json:"Volumes"`
	}
	if err := getJSON(ctx, c, "/volumes", &volumes); err != nil {
		return nil, err
	}

	host, _ := os.Hostname()
	current := currentProject(containers, host)
	if current == "" {
		// Running outside compose (or with a custom hostname): we cannot tell
		// which volumes are "ours", so stay silent rather than guess.
		return nil, errors.New("无法从容器标签识别当前 compose 项目")
	}
	return BuildHint(current, containers, volumes.Volumes), nil
}

func currentProject(containers []dockerContainer, host string) string {
	if host == "" {
		return ""
	}
	for _, ct := range containers {
		if strings.HasPrefix(ct.ID, host) {
			return ct.Labels["com.docker.compose.project"]
		}
		for _, name := range ct.Names {
			if strings.TrimPrefix(name, "/") == host {
				return ct.Labels["com.docker.compose.project"]
			}
		}
	}
	return ""
}

const (
	labelProject    = "com.docker.compose.project"
	labelVolume     = "com.docker.compose.volume"
	labelWorkingDir = "com.docker.compose.project.working_dir"
)

// BuildHint is the pure part of Detect: it keeps only `pgdata` volumes owned by
// other compose projects and enriches them with the bind-mounted data directory
// recorded on those projects' containers (stopped containers included).
func BuildHint(current string, containers []dockerContainer, volumes []dockerVolume) *Hint {
	projects := map[string]*Project{}
	for _, v := range volumes {
		proj := v.Labels[labelProject]
		if proj == "" || proj == current || v.Labels[labelVolume] != "pgdata" {
			continue
		}
		p := projects[proj]
		if p == nil {
			p = &Project{Name: proj}
			projects[proj] = p
		}
		p.Volume = v.Name
		if p.DataDir == "" {
			if wd := v.Labels[labelWorkingDir]; wd != "" {
				p.DataDir = filepath.Join(wd, "data")
			}
		}
	}
	// Containers (even stopped ones) carry the concrete host path of `./data`.
	for _, ct := range containers {
		proj := ct.Labels[labelProject]
		if proj == "" || proj == current {
			continue
		}
		p := projects[proj]
		if p == nil {
			continue // only warn about projects that still own a pgdata volume
		}
		if p.DataDir == "" {
			if wd := ct.Labels[labelWorkingDir]; wd != "" {
				p.DataDir = filepath.Join(wd, "data")
			}
		}
		for _, m := range ct.Mounts {
			if m.Type != "bind" {
				continue
			}
			if m.Destination == "/app/data-redteam" || m.Destination == "/app/data" {
				p.DataDir = m.Source
			}
		}
	}
	h := &Hint{CurrentProject: current}
	for _, p := range projects {
		if p.Volume == "" {
			continue
		}
		h.OldProjects = append(h.OldProjects, *p)
	}
	sort.Slice(h.OldProjects, func(i, j int) bool { return h.OldProjects[i].Name < h.OldProjects[j].Name })
	return h
}

// displayPath normalises Docker Desktop's host-mount prefix. On macOS/Linux
// Docker Desktop the bind source inside the VM is /host_mnt/<host path>; the
// commands we print are meant to be pasted on the host, so strip that prefix.
// On a Linux host there is no prefix and the path is already correct.
func displayPath(p string) string {
	if strings.HasPrefix(p, "/host_mnt/") {
		return strings.TrimPrefix(p, "/host_mnt")
	}
	return p
}

// LogLines renders the multi-line startup warning. Each returned line already
// starts with "[volume]" so callers can log them verbatim.
func LogLines(h *Hint) []string {
	if h == nil || len(h.OldProjects) == 0 {
		return nil
	}
	lines := []string{
		"[volume] ⚠ 检测到其它 compose 项目的数据卷，而本次连接的数据库是新建的空库",
		fmt.Sprintf("[volume]   当前项目：%s", h.CurrentProject),
	}
	for _, p := range h.OldProjects {
		ref := "项目 " + p.Name
		if p.Volume != "" {
			ref += "（卷 " + p.Volume + "）"
		}
		if p.DataDir != "" {
			ref += "，数据目录 " + displayPath(p.DataDir)
		}
		lines = append(lines, "[volume]   旧数据仍在："+ref)
	}
	lines = append(lines, "[volume]   如果这是换目录/改名后的升级，旧数据没有被删除，只是没接上。任选其一接回：")
	for _, p := range h.OldProjects {
		lines = append(lines,
			fmt.Sprintf("[volume]     COMPOSE_PROJECT_NAME=%s docker compose up -d   # 沿用旧项目名（会复用旧 pgdata 卷）", p.Name))
		if p.DataDir != "" {
			lines = append(lines,
				fmt.Sprintf("[volume]     cp -a %s/. ./data/ && COMPOSE_PROJECT_NAME=%s docker compose up -d", displayPath(p.DataDir), p.Name))
		}
	}
	lines = append(lines,
		"[volume]   另外：旧目录下的 data/（traffic 索引、jwt.key 等）不会自动迁移，接回前请手工拷贝到当前目录。",
		"[volume]   本程序不会自动跨卷迁移数据；接回前建议先备份旧卷/旧目录。",
		"[volume]   如果你确认要从空库重新开始，可忽略本提示（它只出现在数据库尚未初始化时）。")
	return lines
}

// Notice is the short one-line version surfaced in the system settings page.
func Notice(h *Hint) string {
	if h == nil || len(h.OldProjects) == 0 {
		return ""
	}
	names := make([]string, 0, len(h.OldProjects))
	for _, p := range h.OldProjects {
		if p.Volume != "" {
			names = append(names, p.Volume)
		} else {
			names = append(names, p.Name)
		}
	}
	msg := fmt.Sprintf("检测到旧部署的数据卷 %s，但本次连接的是新建的空数据库。若你是换目录升级，旧数据仍在原项目卷里；用 COMPOSE_PROJECT_NAME=%s docker compose up -d 可接回（详见启动日志，程序不会自动迁移数据）。",
		strings.Join(names, "、"), h.OldProjects[0].Name)
	return msg
}
