package server

import (
	"log"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/volumecheck"
)

// checkComposeVolumeWarning consumes the hint written by the one-shot
// `volume-check` compose sidecar. It only warns when this process is looking at
// a database that has not been initialised yet: that is exactly the "switched
// compose directories, got a brand-new empty pgdata volume" trap. An existing
// deployment (password already set) never sees this warning, even if unrelated
// compose volumes exist on the machine.
//
// Every failure is downgraded to a log line — detection must never block startup.
func (s *Server) checkComposeVolumeWarning(dataDir string) {
	hint, err := volumecheck.ReadHint(dataDir)
	if err != nil {
		log.Printf("[volume] 读取卷检测结果失败（忽略）：%v", err)
		return
	}
	if hint == nil || len(hint.OldProjects) == 0 {
		return
	}
	if s.m == nil || s.m.pg == nil {
		return
	}
	hash, _, err := s.m.pg.GetSetting(authPassKey)
	if err != nil {
		log.Printf("[volume] 读取初始化状态失败，跳过空库提示：%v", err)
		return
	}
	if hash != "" {
		return
	}
	for _, line := range volumecheck.LogLines(hint) {
		log.Print(line)
	}
	s.volWarn = volumecheck.Notice(hint)
}
