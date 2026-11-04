package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	jwtTTL         = 7 * 24 * time.Hour
	keyChars       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// authUsername is the canonical login name after the ARTEX → ARTEX-RedTeam
	// rename. The web client sends it; legacyAuthUsername stays accepted so old
	// clients (and old bookmarks/scripts) keep working.
	authUsername       = "ARTEX-RedTeam"
	legacyAuthUsername = "ARTEX"

	// authCookie is the browser cookie carrying the JWT. The legacy name is
	// still read so sessions issued before the rename survive until they expire.
	authCookie       = "artex_redteam_token"
	legacyAuthCookie = "artex_token"

	// 下限与 setup 页的前端校验一致——校验只放在前端等于没放，直接打 API 就能
	// 绕过。上限是 bcrypt 的硬限制：超过 72 字节 GenerateFromPassword 会返回
	// ErrPasswordTooLong，提前挡掉好过让用户收到一句含义不明的「密码加密失败」。
	minPasswordRunes = 8
	maxPasswordBytes = 72
)

// errDataSourceUnavailable 是密码相关读操作失败时统一的回复。这些 handler 绝不能
// 把"读不到"当成"没有设置"：authInit 曾因此在数据库报错时放行，让未认证请求覆盖
// 掉已有的管理员密码。
const errDataSourceUnavailable = "数据源暂时不可用，请稍后重试"

// validatePassword 返回空串表示通过，否则返回可直接展示给用户的中文原因。
func validatePassword(pw string) string {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Sprintf("密码长度至少 %d 位", minPasswordRunes)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("密码长度不能超过 %d 字节", maxPasswordBytes)
	}
	return ""
}

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy := filepath.Join(dataDir, jwtKeyFilename); legacy != path {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if data, rerr := os.ReadFile(legacy); rerr == nil {
				if werr := os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf("[auth] JWT key 已从 %s 迁移到 %s（移出可浏览工作区）", legacy, path)
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	buf := make([]byte, 32)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return nil, fmt.Errorf("generate jwt key: %w", err)
		}
		buf[i] = keyChars[n.Int64()]
	}
	if err := os.WriteFile(path, buf, 0600); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf("[auth] 新 JWT key 已写入 %s", path)
	return buf, nil
}

// tokenClaims 在标准声明之外带上密码版本（pv）。
//
// pv 是「当前 bcrypt 哈希」的短哈希：密码一改，pv 就变，签发于旧密码的
// token 全部立即失效。否则改完密码后，攻击者手里的旧 token 还能用满 7 天。
type tokenClaims struct {
	jwt.RegisteredClaims
	PV string `json:"pv,omitempty"`
}

// passwordVersion 把存储的密码哈希折叠成一个不可逆的短版本号。
// 空哈希（尚未初始化）返回空串，此时不做版本校验。
func passwordVersion(hash string) string {
	if hash == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:8])
}

// signJWT issues a 7-day HS256 token for user ARTEX-RedTeam.
// It carries no password version and exists for tests / legacy call sites;
// production issuance goes through signJWTWithPV.
func signJWT(key []byte) (string, error) { return signJWTWithPV(key, "") }

func signJWTWithPV(key []byte, pv string) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   authUsername,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		PV: pv,
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
// wantPV is the password version currently bound to the stored password. An
// empty value on either side means "legacy token" or "not initialised yet":
// the signature is still the gate, so those stay accepted for compatibility
// with tokens issued before the pv claim existed.
func verifyJWT(tokenStr string, key []byte, wantPV string) bool {
	var claims tokenClaims
	t, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	if err != nil || !t.Valid {
		return false
	}
	if claims.PV != "" && wantPV != "" && claims.PV != wantPV {
		return false
	}
	return true
}

// authPVCacheTTL bounds how long a password version can stay cached. The cache
// exists so verification does not hit the DB on every request; the TTL exists
// because the password can also be changed out-of-band (reset-password.sh writes
// the hash straight into the DB), and a permanent cache would then keep accepting
// tokens from the old password or reject freshly issued ones.
const authPVCacheTTL = 30 * time.Second

// currentPasswordVersion returns the pv bound to the stored password. It is
// cached (RLock in the steady state) and refreshed at most once per TTL. On a
// read error it keeps the last known value, or "" if none — the caller then
// skips the version check, but the token still has to be correctly signed.
func (s *Server) currentPasswordVersion() string {
	s.authPVMu.RLock()
	if s.authPVLoaded && time.Since(s.authPVAt) < authPVCacheTTL {
		v := s.authPV
		s.authPVMu.RUnlock()
		return v
	}
	s.authPVMu.RUnlock()

	s.authPVMu.Lock()
	defer s.authPVMu.Unlock()
	if s.authPVLoaded && time.Since(s.authPVAt) < authPVCacheTTL {
		return s.authPV
	}
	if s.m == nil || s.m.pg == nil {
		return s.authPV
	}
	hash, _, err := s.m.pg.GetSetting(authPassKey)
	if err != nil {
		return s.authPV
	}
	s.authPV = passwordVersion(hash)
	s.authPVLoaded = true
	s.authPVAt = time.Now()
	return s.authPV
}

// cachePasswordVersion records the pv of a hash we just wrote, so the next
// request does not have to round-trip to the DB.
func (s *Server) cachePasswordVersion(hash string) {
	s.authPVMu.Lock()
	s.authPV = passwordVersion(hash)
	s.authPVLoaded = true
	s.authPVAt = time.Now()
	s.authPVMu.Unlock()
}

// extractToken reads the JWT from Authorization: Bearer header, the
// artex_redteam_token cookie (legacy artex_token accepted), or ?token= query
// param (for SSE connections).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie(authCookie); err == nil && c.Value != "" {
		return c.Value
	}
	if c, err := r.Cookie(legacyAuthCookie); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// publicAPIPaths are the only paths served without a JWT. Keep this an exact
// allowlist rather than an "/api/auth/" prefix: a prefix means any future
// handler added under auth is public by default, which is how one fail-open bug
// turns into a full takeover. change-password validates the token itself and is
// also fine to route through the middleware.
var publicAPIPaths = map[string]struct{}{
	"/api/auth/status": {},
	"/api/auth/init":   {},
	"/api/auth/login":  {},
	"/api/health":      {},
}

// requireAuth wraps h with JWT validation.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := publicAPIPaths[r.URL.Path]; ok {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, "未授权")
			return
		}
		if !verifyJWT(tok, s.jwtKey, s.currentPasswordVersion()) {
			writeErr(w, 401, "token 无效或已过期")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// 读失败必须回 503 而不是 initialized:false：前端在 initialized:false 时会把用户
// 送到 /setup 去设置密码（login/page.tsx），把数据库故障包装成 200 等于把用户往
// 覆盖已有密码的路上推。
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"initialized": hash != ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already set.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	existing, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if existing != "" {
		writeErr(w, 403, "密码已设置")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeStrict(r, &req); err != nil || req.Password == "" {
		writeErr(w, 400, "密码不能为空")
		return
	}
	if msg := validatePassword(req.Password); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	// 用 INSERT ... ON CONFLICT DO NOTHING 而不是 upsert：上面那次 GetSetting 只是
	// 快速失败路径，真正"仅首次可设"的保证落在主键约束上。bcrypt 要跑几十毫秒，
	// 这期间别的请求完全可能先把密码设好，而读检查本身也可能因故障而失效。
	inserted, err := pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	if !inserted {
		writeErr(w, 403, "密码已设置")
		return
	}
	s.cachePasswordVersion(string(hash))
	tok, err := signJWTWithPV(s.jwtKey, passwordVersion(string(hash)))
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token AND the current password. Changing the password rotates the password
// version, revoking every previously issued token; the response carries a fresh
// one for the caller.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !verifyJWT(extractToken(r), s.jwtKey, s.currentPasswordVersion()) {
		writeErr(w, 401, "未授权")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeStrict(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, "新密码不能为空")
		return
	}
	if msg := validatePassword(req.NewPassword); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		writeErr(w, 401, "当前密码错误")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 密码变了，pv 随之变化：所有旧 token 立即失效。给当前会话换发一枚新
	// token，前端保存后不会被踢回登录页。
	s.cachePasswordVersion(string(newHash))
	tok, err := signJWTWithPV(s.jwtKey, passwordVersion(string(newHash)))
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "token": tok})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeStrict(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if req.Username != authUsername && req.Username != legacyAuthUsername {
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	s.cachePasswordVersion(hash)
	tok, err := signJWTWithPV(s.jwtKey, passwordVersion(hash))
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
