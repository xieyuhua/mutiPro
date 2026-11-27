package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// 会话有效期。到期后需要重新登录。
const webSessionTTL = 12 * time.Hour

// 登录失败的最大尝试次数与封禁时长，防止暴力猜密码。
const (
	webMaxLoginAttempts = 5
	webLoginBanDuration = 5 * time.Minute
)

// webServer 提供局域网内的管理页面。
//
// 只监听 HTTP，不做 TLS：局域网场景下配置证书成本过高，
// 因此靠「密码 + 会话」保护。若需要跨公网访问，应通过
// 反向代理加上 HTTPS。
type webServer struct {
	mu sync.Mutex

	mw       *mainWindow
	server   *http.Server
	listener net.Listener
	running  bool
	port     int
	lastErr  string

	// sessions 保存有效的会话令牌与过期时间。
	sessions map[string]time.Time
	// attempts 记录每个来源 IP 的连续登录失败次数与封禁截止时间。
	attempts map[string]*loginAttempt
}

type loginAttempt struct {
	count    int
	banUntil time.Time
}

func newWebServer(mw *mainWindow) *webServer {
	return &webServer{
		mw:       mw,
		sessions: map[string]time.Time{},
		attempts: map[string]*loginAttempt{},
	}
}

// Status 返回当前运行状态，供界面显示。
func (s *webServer) Status() (running bool, port int, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.port, s.lastErr
}

// Start 在指定端口启动服务。端口被占用时返回错误。
func (s *webServer) Start(port int) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("服务已在运行")
	}
	s.mu.Unlock()

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		s.setError(err)
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/programs", s.requireAuth(s.handlePrograms))
	mux.HandleFunc("/api/log", s.requireAuth(s.handleLog))
	mux.HandleFunc("/api/action", s.requireAuth(s.handleAction))

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// 请求体很小，限制大小避免恶意请求占用内存。
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// 传入 0 时由系统分配端口，实际端口要从 listener 读取，
	// 否则状态显示与实际监听不一致。
	actualPort := port
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		actualPort = addr.Port
	}

	s.mu.Lock()
	s.server = server
	s.listener = listener
	s.running = true
	s.port = actualPort
	s.lastErr = ""
	s.mu.Unlock()

	go func() {
		err := server.Serve(listener)
		s.mu.Lock()
		s.running = false
		if err != nil && err != http.ErrServerClosed {
			s.lastErr = err.Error()
		}
		s.mu.Unlock()
	}()
	return nil
}

func (s *webServer) setError(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// Stop 关闭服务并清理会话。
func (s *webServer) Stop() {
	s.mu.Lock()
	server := s.server
	running := s.running
	s.sessions = map[string]time.Time{}
	s.attempts = map[string]*loginAttempt{}
	s.mu.Unlock()

	if server != nil && running {
		_ = server.Close()
	}
	s.mu.Lock()
	s.running = false
	s.server = nil
	s.listener = nil
	s.mu.Unlock()
}

// Restart 用于端口变更后重启。
func (s *webServer) Restart(port int) error {
	s.Stop()
	return s.Start(port)
}

// ---------------------------------------------------------------- 认证

// newSession 生成并登记一个会话令牌。
func (s *webServer) newSession() string {
	token := randomPassword(32)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(webSessionTTL)
	// 顺手清理过期会话，避免无限增长。
	now := time.Now()
	for key, expiry := range s.sessions {
		if now.After(expiry) {
			delete(s.sessions, key)
		}
	}
	s.mu.Unlock()
	return token
}

// validSession 校验会话令牌是否有效。
func (s *webServer) validSession(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.sessions, token)
		return false
	}
	return true
}

// dropSession 注销指定会话。
func (s *webServer) dropSession(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// sessionCookieName 是保存会话令牌的 Cookie 名。
const sessionCookieName = "multipro_session"

// requireAuth 包装需要登录的接口。
func (s *webServer) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !s.validSession(cookie.Value) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "未登录或会话已过期",
			})
			return
		}
		next(w, r)
	}
}

// clientIP 取出来源地址（不含端口），用于登录限流。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// checkLoginAllowed 判断该来源是否因失败过多被封禁。
func (s *webServer) checkLoginAllowed(ip string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[ip]
	if !ok {
		return true, 0
	}
	if time.Now().Before(attempt.banUntil) {
		return false, time.Until(attempt.banUntil)
	}
	return true, 0
}

// recordLoginFailure 记录失败次数，达到阈值后封禁。
func (s *webServer) recordLoginFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[ip]
	if !ok {
		attempt = &loginAttempt{}
		s.attempts[ip] = attempt
	}
	attempt.count++
	if attempt.count >= webMaxLoginAttempts {
		attempt.banUntil = time.Now().Add(webLoginBanDuration)
		attempt.count = 0
	}
}

// clearLoginFailures 登录成功后清除失败记录。
func (s *webServer) clearLoginFailures(ip string) {
	s.mu.Lock()
	delete(s.attempts, ip)
	s.mu.Unlock()
}

// ---------------------------------------------------------------- HTTP 处理

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// 管理接口不缓存，避免浏览器显示过期状态。
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(webPageHTML))
}

func (s *webServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	ip := clientIP(r)
	if allowed, wait := s.checkLoginAllowed(ip); !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": fmt.Sprintf("尝试次数过多，请 %d 秒后再试", int(wait.Seconds())+1),
		})
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}

	expected := s.mw.app.WebPassword
	// 定长比较，避免通过响应时间猜测密码。
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(expected)) != 1 {
		s.recordLoginFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密码不正确"})
		return
	}

	s.clearLoginFailures(ip)
	token := s.newSession()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// SameSite Lax：允许从浏览器直接打开页面，同时阻止跨站表单提交。
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(webSessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *webServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.dropSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// webProgram 是返回给前端的程序状态。
type webProgram struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Running  bool   `json:"running"`
	PID      int    `json:"pid"`
	Uptime   string `json:"uptime"`
	Type     string `json:"type"`
	Schedule string `json:"schedule"`
}

func (s *webServer) handlePrograms(resp http.ResponseWriter, r *http.Request) {
	// 用快照遍历：切片可能在 UI 线程增删，Runner 自身状态由其锁保护。
	// 不依赖 UI 消息循环，HTTP 处理可以独立于界面。
	runners := s.mw.runnersSnapshot()
	programs := make([]webProgram, 0, len(runners))
	for _, runner := range runners {
		cfg := runner.Config()
		state := runner.State()
		item := webProgram{
			ID:       cfg.ID,
			Name:     cfg.Name,
			State:    state.String(),
			Running:  state == StateRunning,
			PID:      runner.PID(),
			Type:     cfg.ScriptLabel(),
			Schedule: runner.ScheduleSummary(),
		}
		if uptime := runner.Uptime(); uptime > 0 {
			item.Uptime = formatUptime(uptime)
		}
		programs = append(programs, item)
	}
	writeJSON(resp, http.StatusOK, map[string]interface{}{
		"programs": programs,
		"hostname": hostnameSafe(),
	})
}

func (s *webServer) handleLog(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 id"})
		return
	}
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 && n <= 2000 {
			tail = n
		}
	}

	// 直接从快照查找，不依赖 UI 消息循环。
	var runner *Runner
	for _, candidate := range s.mw.runnersSnapshot() {
		if candidate.Config().ID == id {
			runner = candidate
			break
		}
	}
	if runner == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "程序不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":   id,
		"name": runner.Config().Name,
		"log":  toCRLF(strings.Join(runner.LogTail(tail), "\n")),
	})
}

func (s *webServer) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var body struct {
		Action string `json:"action"`
		ID     string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}

	message, ok := s.mw.performWebAction(body.Action, body.ID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": message})
}

// parseInt 是 strconv.Atoi 的薄包装，避免在多个文件重复引入。
func parseInt(text string) (int, error) {
	var value int
	_, err := fmt.Sscanf(strings.TrimSpace(text), "%d", &value)
	return value, err
}

// hostname 返回本机名，用于页面上标明是哪台机器。
func hostname() (string, error) {
	return os.Hostname()
}

func hostnameSafe() string {
	name, err := hostname()
	if err != nil {
		return ""
	}
	return name
}

// LocalAddresses 返回本机在局域网中的 IPv4 地址，用于提示访问方式。
func LocalAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil {
				continue
			}
			result = append(result, ip.String())
		}
	}
	sort.Strings(result)
	return result
}
