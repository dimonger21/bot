// +build !windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
)

// ============================================
// КОНФИГУРАЦИЯ
// ============================================

type Config struct {
	Token             string
	Debug             bool
	OwnerID           int64
	DeepSeekAPI       string
	LogsGroupID       int64
	ReviewsGroupID    int64
	PromotionsGroupID int64
	WebPort           string
	WebUsername       string
	WebPassword       string
}

func LoadConfig() *Config {
	return &Config{
		Token:             getEnv("TELEGRAM_TOKEN", ""),
		Debug:             getEnvBool("DEBUG", false),
		OwnerID:           getEnvInt64("OWNER_ID", 0),
		DeepSeekAPI:       getEnv("DEEPSEEK_API", ""),
		LogsGroupID:       getEnvInt64("LOGS_GROUP_ID", 0),
		ReviewsGroupID:    getEnvInt64("REVIEWS_GROUP_ID", 0),
		PromotionsGroupID: getEnvInt64("PROMOTIONS_GROUP_ID", 0),
		WebPort:           getEnv("WEB_PORT", "8080"),
		WebUsername:       getEnv("WEB_USERNAME", "admin"),
		WebPassword:       getEnv("WEB_PASSWORD", "admin123"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return value == "true" || value == "1" || value == "yes"
	}
	return defaultValue
}

func getEnvInt64(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i
		}
	}
	return defaultValue
}

var config = LoadConfig()

// ============================================
// МОДЕЛИ
// ============================================

type User struct {
	ID               int64
	Username         string
	FirstName        string
	LastName         string
	RoleID           string
	Prefix           string
	RegisteredAt     time.Time
	Reputation       int
	DaysInTeam       int
	TicketsCount     int
	Rating           float64
	IsActive         bool
	IsBanned         bool
	IsMuted          bool
	MuteUntil        *time.Time
	BanReason        string
	PromotionHistory []PromotionLog
}

type Role struct {
	ID             string
	Name           string
	Prefix         string
	Level          int
	Permissions    []string
	CanSeeLogs     bool
	CanManageRoles bool
	CanBan         bool
	CanMute        bool
	CreatedAt      time.Time
	CreatedBy      int64
}

type Ticket struct {
	ID          string
	UserID      int64
	User        string
	AdminID     int64
	AdminName   string
	Type        string
	Category    string
	Status      string
	Message     string
	CreatedAt   time.Time
	ClosedAt    *time.Time
	Rating      *int
	ReviewText  string
	IsActive    bool
}

type Message struct {
	ID        int
	TicketID  string
	FromID    int64
	ToID      int64
	Text      string
	IsAdmin   bool
	Timestamp time.Time
}

type PromotionLog struct {
	Timestamp      time.Time
	UserID         int64
	UserName       string
	OldRole        string
	NewRole        string
	OldPrefix      string
	NewPrefix      string
	Reason         string
	PromotedBy     int64
	PromotedByName string
	Action         string
}

type BanLog struct {
	Timestamp time.Time
	UserID    int64
	UserName  string
	Action    string
	Duration  string
	Reason    string
	AdminID   int64
	AdminName string
}

type TechnicalRequest struct {
	ID          string
	UserID      int64
	Username    string
	Category    string
	Description string
	Status      string
	CreatedAt   time.Time
	ReviewedAt  *time.Time
	ReviewedBy  int64
}

type UserState struct {
	Step          string
	TicketID      string
	TechnicalType string
	TempMessage   string
}

type WebUser struct {
	ID        int64
	Username  string
	Password  string
	Role      string
	CreatedAt time.Time
}

type Session struct {
	UserID    int64
	ExpiresAt time.Time
}

// ============================================
// ХРАНИЛИЩЕ
// ============================================

type Storage struct {
	mu                sync.RWMutex
	users             map[int64]*User
	roles             map[string]*Role
	tickets           map[string]*Ticket
	messages          map[string][]*Message
	techRequests      map[string]*TechnicalRequest
	logs              []*PromotionLog
	banLogs           []*BanLog
	userStates        map[int64]*UserState
	webUsers          map[string]*WebUser
	sessions          map[string]*Session
	ticketCounter     int
	technicalCounter  int
}

func NewStorage() *Storage {
	s := &Storage{
		users:            make(map[int64]*User),
		roles:            make(map[string]*Role),
		tickets:          make(map[string]*Ticket),
		messages:         make(map[string][]*Message),
		techRequests:     make(map[string]*TechnicalRequest),
		logs:             make([]*PromotionLog, 0),
		banLogs:          make([]*BanLog, 0),
		userStates:       make(map[int64]*UserState),
		webUsers:         make(map[string]*WebUser),
		sessions:         make(map[string]*Session),
		ticketCounter:    1000,
		technicalCounter: 500,
	}

	s.createDefaultRoles()
	s.createDefaultWebUser()
	return s
}

func (s *Storage) createDefaultRoles() {
	roles := []*Role{
		{
			ID:             "owner",
			Name:           "Владелец",
			Prefix:         "👑",
			Level:          100,
			Permissions:    []string{"all"},
			CanSeeLogs:     true,
			CanManageRoles: true,
			CanBan:         true,
			CanMute:        true,
			CreatedAt:      time.Now(),
			CreatedBy:      0,
		},
		{
			ID:             "technical_admin",
			Name:           "Технический администратор",
			Prefix:         "🛠",
			Level:          80,
			Permissions:    []string{"view_logs", "manage_technical", "view_users"},
			CanSeeLogs:     true,
			CanManageRoles: false,
			CanBan:         false,
			CanMute:        false,
			CreatedAt:      time.Now(),
			CreatedBy:      0,
		},
		{
			ID:             "senior_admin",
			Name:           "Главный администратор",
			Prefix:         "⭐",
			Level:          70,
			Permissions:    []string{"view_logs", "manage_tickets", "view_users", "manage_team"},
			CanSeeLogs:     true,
			CanManageRoles: false,
			CanBan:         true,
			CanMute:        true,
			CreatedAt:      time.Now(),
			CreatedBy:      0,
		},
		{
			ID:             "support_admin",
			Name:           "Администратор поддержки",
			Prefix:         "💬",
			Level:          50,
			Permissions:    []string{"manage_tickets", "view_users"},
			CanSeeLogs:     false,
			CanManageRoles: false,
			CanBan:         false,
			CanMute:        false,
			CreatedAt:      time.Now(),
			CreatedBy:      0,
		},
		{
			ID:             "helper",
			Name:           "Помощник",
			Prefix:         "🌱",
			Level:          30,
			Permissions:    []string{"view_tickets"},
			CanSeeLogs:     false,
			CanManageRoles: false,
			CanBan:         false,
			CanMute:        false,
			CreatedAt:      time.Now(),
			CreatedBy:      0,
		},
	}
	for _, role := range roles {
		s.roles[role.ID] = role
	}
}

func (s *Storage) createDefaultWebUser() {
	hash := sha256.Sum256([]byte(config.WebPassword))
	s.webUsers[config.WebUsername] = &WebUser{
		ID:        1,
		Username:  config.WebUsername,
		Password:  hex.EncodeToString(hash[:]),
		Role:      "owner",
		CreatedAt: time.Now(),
	}
}

// Методы хранилища
func (s *Storage) SaveUser(user *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[user.ID] = user
	return nil
}

func (s *Storage) GetUser(id int64) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, exists := s.users[id]
	if !exists {
		return nil, errors.New("пользователь не найден")
	}
	return user, nil
}

func (s *Storage) UpdateUser(user *User) error {
	return s.SaveUser(user)
}

func (s *Storage) GetAllUsers() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var users []*User
	for _, user := range s.users {
		users = append(users, user)
	}
	return users
}

func (s *Storage) GetAllAdmins() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var admins []*User
	for _, user := range s.users {
		if user.RoleID != "" && user.IsActive && !user.IsBanned {
			admins = append(admins, user)
		}
	}
	return admins
}

func (s *Storage) GetRole(id string) (*Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	role, exists := s.roles[id]
	if !exists {
		return nil, errors.New("роль не найдена")
	}
	return role, nil
}

func (s *Storage) GetAllRoles() []*Role {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var roles []*Role
	for _, role := range s.roles {
		roles = append(roles, role)
	}
	return roles
}

func (s *Storage) SaveRole(role *Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[role.ID] = role
	return nil
}

func (s *Storage) DeleteRole(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.roles, id)
	return nil
}

func (s *Storage) SaveTicket(ticket *Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[ticket.ID] = ticket
	return nil
}

func (s *Storage) GetTicket(id string) (*Ticket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ticket, exists := s.tickets[id]
	if !exists {
		return nil, errors.New("тикет не найден")
	}
	return ticket, nil
}

func (s *Storage) UpdateTicket(ticket *Ticket) error {
	return s.SaveTicket(ticket)
}

func (s *Storage) GetActiveTickets() []*Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var tickets []*Ticket
	for _, ticket := range s.tickets {
		if ticket.IsActive && ticket.Status != "closed" {
			tickets = append(tickets, ticket)
		}
	}
	return tickets
}

func (s *Storage) GetUserTickets(userID int64) []*Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var tickets []*Ticket
	for _, ticket := range s.tickets {
		if ticket.UserID == userID {
			tickets = append(tickets, ticket)
		}
	}
	return tickets
}

func (s *Storage) SaveMessage(msg *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages[msg.TicketID] == nil {
		s.messages[msg.TicketID] = make([]*Message, 0)
	}
	s.messages[msg.TicketID] = append(s.messages[msg.TicketID], msg)
	return nil
}

func (s *Storage) GetTicketMessages(ticketID string) []*Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.messages[ticketID]
}

func (s *Storage) SaveTechnicalRequest(req *TechnicalRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.techRequests[req.ID] = req
	return nil
}

func (s *Storage) GetTechnicalRequest(id string) (*TechnicalRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	req, exists := s.techRequests[id]
	if !exists {
		return nil, errors.New("заявка не найдена")
	}
	return req, nil
}

func (s *Storage) GetPendingTechnicalRequests() []*TechnicalRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var requests []*TechnicalRequest
	for _, req := range s.techRequests {
		if req.Status == "pending" {
			requests = append(requests, req)
		}
	}
	return requests
}

func (s *Storage) AddPromotionLog(log *PromotionLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, log)
}

func (s *Storage) GetPromotionLogs() []*PromotionLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs
}

func (s *Storage) AddBanLog(log *BanLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.banLogs = append(s.banLogs, log)
}

func (s *Storage) GetBanLogs() []*BanLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.banLogs
}

func (s *Storage) GetUserState(userID int64) *UserState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if state, exists := s.userStates[userID]; exists {
		return state
	}
	return &UserState{Step: "start"}
}

func (s *Storage) SetUserState(userID int64, state *UserState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userStates[userID] = state
}

func (s *Storage) ClearUserState(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.userStates, userID)
}

func (s *Storage) GenerateTicketID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ticketCounter++
	return strconv.Itoa(s.ticketCounter)
}

func (s *Storage) GenerateTechnicalID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.technicalCounter++
	return strconv.Itoa(s.technicalCounter)
}

func (s *Storage) GetWebUser(username string) (*WebUser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, exists := s.webUsers[username]
	if !exists {
		return nil, errors.New("пользователь не найден")
	}
	return user, nil
}

func (s *Storage) SetSession(sessionID string, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = &Session{
		UserID:    userID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func (s *Storage) GetSession(sessionID string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[sessionID]
	if !exists {
		return nil, errors.New("сессия не найдена")
	}
	if session.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("сессия истекла")
	}
	return session, nil
}

func (s *Storage) DeleteSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// ============================================
// КЛАВИАТУРЫ
// ============================================

func GetMainKeyboard() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("💬 Написать администратору"),
			tgbotapi.NewKeyboardButton("📞 Техническая поддержка"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("📂 Мои обращения"),
			tgbotapi.NewKeyboardButton("ℹ️ Информация"),
		),
	)
}

func GetAdminKeyboard() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("📋 Активные обращения"),
			tgbotapi.NewKeyboardButton("👥 Команда"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("📊 Статистика"),
			tgbotapi.NewKeyboardButton("👤 Мой профиль"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("🧑‍💻 Админ панель"),
			tgbotapi.NewKeyboardButton("📜 История действий"),
		),
	)
}

func GetOwnerKeyboard() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("⚙️ Настройки"),
			tgbotapi.NewKeyboardButton("👑 Управление ролями"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("📜 Логи"),
			tgbotapi.NewKeyboardButton("📊 Полная статистика"),
		),
	)
}

func GetTechnicalCategoriesKeyboard() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("👨‍💻 Заявка на администратора"),
			tgbotapi.NewKeyboardButton("🐛 Ошибка"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("💡 Предложение"),
			tgbotapi.NewKeyboardButton("🤝 Сотрудничество"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("❓ Другое"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("🔙 Назад"),
		),
	)
}

// ============================================
// ОСНОВНОЙ БОТ
// ============================================

var (
	bot     *tgbotapi.BotAPI
	storage *Storage
)

func main() {
	log.Println("🚀 Запуск Relax Bot...")

	if config.Token == "" {
		log.Fatal("❌ TELEGRAM_TOKEN не задан!")
	}

	var err error
	bot, err = tgbotapi.NewBotAPI(config.Token)
	if err != nil {
		log.Fatal("❌ Ошибка инициализации бота:", err)
	}

	bot.Debug = config.Debug
	log.Printf("✅ Бот авторизован как %s", bot.Self.UserName)

	storage = NewStorage()
	log.Println("💾 Хранилище инициализировано")

	if config.OwnerID != 0 {
		owner := &User{
			ID:           config.OwnerID,
			FirstName:    "Владелец",
			RoleID:       "owner",
			Prefix:       "👑",
			RegisteredAt: time.Now(),
			IsActive:     true,
			Reputation:   999,
			DaysInTeam:   0,
			TicketsCount: 0,
			Rating:       5.0,
		}
		storage.SaveUser(owner)
		log.Printf("👑 Владелец добавлен: %d", config.OwnerID)
	}

	go startWebServer()

	log.Println("📡 Запуск бота...")
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		go handleUpdate(update)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Бот остановлен")
}

// ============================================
// ВЕБ-СЕРВЕР
// ============================================

func startWebServer() {
	port := config.WebPort
	if port == "" {
		port = "8080"
	}

	log.Printf("🌐 Веб-сервер на порту %s", port)
	log.Printf("🔑 Логин: %s | Пароль: %s", config.WebUsername, config.WebPassword)

	http.HandleFunc("/", handleWebRoot)
	http.HandleFunc("/login", handleWebLogin)
	http.HandleFunc("/logout", handleWebLogout)
	http.HandleFunc("/dashboard", authMiddleware(handleWebDashboard))
	http.HandleFunc("/logs", authMiddleware(handleWebLogs))
	http.HandleFunc("/console", authMiddleware(handleWebConsole))
	http.HandleFunc("/api/logs", authMiddleware(handleAPILogs))
	http.HandleFunc("/api/users", authMiddleware(handleAPIUsers))
	http.HandleFunc("/api/ban", authMiddleware(handleAPIBan))
	http.HandleFunc("/api/mute", authMiddleware(handleAPIMute))
	http.HandleFunc("/api/promote", authMiddleware(handleAPIPromote))
	http.HandleFunc("/api/roles", authMiddleware(handleAPIRoles))

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Printf("❌ Ошибка веб-сервера: %v", err)
	}
}

// ============================================
// ВЕБ-ОБРАБОТЧИКИ
// ============================================

func handleWebRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func handleWebLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		tmpl := `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>🌿 Relax - Вход</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;
background:linear-gradient(135deg,#667eea 0%,#764ba2 100%);height:100vh;display:flex;justify-content:center;align-items:center}
.login-container{background:white;padding:40px;border-radius:16px;box-shadow:0 20px 60px rgba(0,0,0,0.3);width:100%;max-width:400px}
h1{color:#2d3748;margin-bottom:8px;font-size:28px}
.subtitle{color:#718096;margin-bottom:30px;font-size:14px}
.form-group{margin-bottom:20px}
label{display:block;color:#2d3748;margin-bottom:6px;font-weight:500;font-size:14px}
input{width:100%;padding:12px 16px;border:2px solid #e2e8f0;border-radius:8px;font-size:16px}
input:focus{outline:none;border-color:#667eea}
button{width:100%;padding:12px;background:linear-gradient(135deg,#667eea 0%,#764ba2 100%);
color:white;border:none;border-radius:8px;font-size:16px;font-weight:600;cursor:pointer}
button:hover{transform:translateY(-2px)}
.error{color:#e53e3e;margin-top:12px;font-size:14px}
.logo{font-size:48px;margin-bottom:16px}
</style>
</head>
<body>
<div class="login-container">
<div class="logo">🌿</div>
<h1>Relax Panel</h1>
<p class="subtitle">Войдите в панель управления</p>
<form method="POST">
<div class="form-group"><label>Логин</label><input type="text" name="username" required></div>
<div class="form-group"><label>Пароль</label><input type="password" name="password" required></div>
<button type="submit">Войти</button>
{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
</form>
</div>
</body>
</html>`
		t := template.Must(template.New("login").Parse(tmpl))
		t.Execute(w, nil)
		return
	}

	if r.Method == "POST" {
		username := r.FormValue("username")
		password := r.FormValue("password")

		webUser, err := storage.GetWebUser(username)
		if err != nil {
			tmpl := template.Must(template.New("login").Parse(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Вход</title><style>*{margin:0;padding:0;box-sizing:border-box}body{font-family:sans-serif;background:linear-gradient(135deg,#667eea 0%,#764ba2 100%);height:100vh;display:flex;justify-content:center;align-items:center}.login-container{background:white;padding:40px;border-radius:16px;box-shadow:0 20px 60px rgba(0,0,0,0.3);width:100%;max-width:400px}h1{color:#2d3748;margin-bottom:30px}.form-group{margin-bottom:20px}input{width:100%;padding:12px;border:2px solid #e2e8f0;border-radius:8px}button{width:100%;padding:12px;background:linear-gradient(135deg,#667eea,#764ba2);color:white;border:none;border-radius:8px;cursor:pointer}.error{color:#e53e3e;margin-top:12px}</style></head><body><div class="login-container"><h1>🌿 Relax</h1><form method="POST"><div class="form-group"><input type="text" name="username" placeholder="Логин" required></div><div class="form-group"><input type="password" name="password" placeholder="Пароль" required></div><button type="submit">Войти</button><div class="error">Неверный логин или пароль</div></form></div></body></html>`))
			t.Execute(w, map[string]interface{}{"Error": "Неверный логин или пароль"})
			return
		}

		hash := sha256.Sum256([]byte(password))
		if hex.EncodeToString(hash[:]) != webUser.Password {
			tmpl := template.Must(template.New("login").Parse(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Вход</title><style>*{margin:0;padding:0;box-sizing:border-box}body{font-family:sans-serif;background:linear-gradient(135deg,#667eea 0%,#764ba2 100%);height:100vh;display:flex;justify-content:center;align-items:center}.login-container{background:white;padding:40px;border-radius:16px;box-shadow:0 20px 60px rgba(0,0,0,0.3);width:100%;max-width:400px}h1{color:#2d3748;margin-bottom:30px}.form-group{margin-bottom:20px}input{width:100%;padding:12px;border:2px solid #e2e8f0;border-radius:8px}button{width:100%;padding:12px;background:linear-gradient(135deg,#667eea,#764ba2);color:white;border:none;border-radius:8px;cursor:pointer}.error{color:#e53e3e;margin-top:12px}</style></head><body><div class="login-container"><h1>🌿 Relax</h1><form method="POST"><div class="form-group"><input type="text" name="username" placeholder="Логин" required></div><div class="form-group"><input type="password" name="password" placeholder="Пароль" required></div><button type="submit">Войти</button><div class="error">Неверный логин или пароль</div></form></div></body></html>`))
			t.Execute(w, map[string]interface{}{"Error": "Неверный логин или пароль"})
			return
		}

		sessionID := generateSessionID()
		storage.SetSession(sessionID, webUser.ID)

		http.SetCookie(w, &http.Cookie{
			Name:    "session_id",
			Value:   sessionID,
			Expires: time.Now().Add(24 * time.Hour),
			Path:    "/",
		})

		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}
}

func handleWebLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_id")
	if err == nil {
		storage.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:    "session_id",
		Value:   "",
		Expires: time.Now().Add(-1 * time.Hour),
		Path:    "/",
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func handleWebDashboard(w http.ResponseWriter, r *http.Request) {
	userID := getUserIDFromSession(r)
	user, _ := storage.GetUser(userID)

	tmpl := `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>🌿 Relax - Панель</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#f7fafc}
.header{background:white;padding:20px 40px;box-shadow:0 2px 4px rgba(0,0,0,0.1);display:flex;justify-content:space-between;align-items:center}
.header h1{color:#2d3748;font-size:24px}
.header a{color:#667eea;text-decoration:none;margin-left:20px}
.container{padding:40px;max-width:1400px;margin:0 auto}
.stats-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:20px;margin-bottom:40px}
.stat-card{background:white;padding:24px;border-radius:12px;box-shadow:0 2px 8px rgba(0,0,0,0.08)}
.stat-card .number{font-size:32px;font-weight:700;color:#2d3748}
.stat-card .label{color:#718096;font-size:14px;margin-top:4px}
.menu-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:20px}
.menu-item{background:white;padding:30px;border-radius:12px;text-align:center;box-shadow:0 2px 8px rgba(0,0,0,0.08);transition:transform 0.2s;cursor:pointer;text-decoration:none;color:#2d3748;display:block}
.menu-item:hover{transform:translateY(-4px);box-shadow:0 4px 16px rgba(0,0,0,0.12)}
.menu-item .icon{font-size:40px;display:block;margin-bottom:12px}
.menu-item .title{font-weight:600;font-size:16px}
</style>
</head>
<body>
<div class="header"><h1>🌿 Relax Panel</h1><div><span>{{.User.FirstName}}</span><a href="/logout">Выйти</a></div></div>
<div class="container">
<div class="stats-grid">
<div class="stat-card"><div class="number">{{.Stats.Users}}</div><div class="label">Пользователей</div></div>
<div class="stat-card"><div class="number">{{.Stats.Tickets}}</div><div class="label">Обращений</div></div>
<div class="stat-card"><div class="number">{{.Stats.Admins}}</div><div class="label">Администраторов</div></div>
<div class="stat-card"><div class="number">{{.Stats.ActiveTickets}}</div><div class="label">Активных</div></div>
</div>
<div class="menu-grid">
<a href="/logs" class="menu-item"><span class="icon">📜</span><div class="title">Логи</div></a>
{{if .IsOwner}}<a href="/console" class="menu-item"><span class="icon">🖥️</span><div class="title">Консоль</div></a>{{end}}
</div>
</div>
</body>
</html>`

	type Stats struct {
		Users         int
		Tickets       int
		Admins        int
		ActiveTickets int
	}

	stats := Stats{
		Users:         len(storage.GetAllUsers()),
		Tickets:       len(storage.tickets),
		Admins:        len(storage.GetAllAdmins()),
		ActiveTickets: len(storage.GetActiveTickets()),
	}

	data := map[string]interface{}{
		"User":    user,
		"Stats":   stats,
		"IsOwner": user != nil && user.RoleID == "owner",
	}

	t := template.Must(template.New("dashboard").Parse(tmpl))
	t.Execute(w, data)
}

func handleWebLogs(w http.ResponseWriter, r *http.Request) {
	userID := getUserIDFromSession(r)
	user, _ := storage.GetUser(userID)

	role, _ := storage.GetRole(user.RoleID)
	if role == nil || !role.CanSeeLogs {
		http.Error(w, "Доступ запрещен", http.StatusForbidden)
		return
	}

	tmpl := `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>🌿 Relax - Логи</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#f7fafc}
.header{background:white;padding:20px 40px;box-shadow:0 2px 4px rgba(0,0,0,0.1);display:flex;justify-content:space-between;align-items:center}
.header h1{color:#2d3748;font-size:24px}.header a{color:#667eea;text-decoration:none}
.container{padding:40px;max-width:1400px;margin:0 auto}
.filters{background:white;padding:20px;border-radius:12px;margin-bottom:20px;display:flex;gap:20px;align-items:center;flex-wrap:wrap}
.filters select,.filters input{padding:8px 12px;border:2px solid #e2e8f0;border-radius:8px}
.log-table{background:white;border-radius:12px;overflow:auto;box-shadow:0 2px 8px rgba(0,0,0,0.08)}
table{width:100%;border-collapse:collapse}
th{background:#f7fafc;padding:12px 16px;text-align:left;font-weight:600;color:#2d3748;font-size:14px}
td{padding:12px 16px;border-top:1px solid #e2e8f0;font-size:14px;color:#2d3748}
.badge{padding:4px 12px;border-radius:20px;font-size:12px;font-weight:600}
.badge-promotion{background:#48bb78;color:white}
.badge-ban{background:#fc8181;color:white}
.badge-mute{background:#ed8936;color:white}
.badge-other{background:#a0aec0;color:white}
.timestamp{color:#718096;font-size:13px}
</style>
</head>
<body>
<div class="header"><h1>📜 Логи</h1><a href="/dashboard">← Назад</a></div>
<div class="container">
<div class="filters">
<select id="typeFilter" onchange="filterLogs()">
<option value="all">Все</option>
<option value="promotion">Повышения</option>
<option value="ban">Баны</option>
<option value="mute">Муты</option>
</select>
<input type="text" id="searchInput" placeholder="Поиск..." onkeyup="filterLogs()">
</div>
<div class="log-table"><table><thead><tr><th>Время</th><th>Тип</th><th>Действие</th><th>Кем</th></tr></thead>
<tbody id="logBody">
{{range .Logs}}
<tr><td class="timestamp">{{.Timestamp.Format "02.01.2006 15:04"}}</td>
<td><span class="badge {{.BadgeClass}}">{{.Type}}</span></td>
<td>{{.Details}}</td><td>{{.AdminName}}</td></tr>
{{end}}
</tbody></table></div></div>
<script>
function filterLogs(){var t=document.getElementById('typeFilter').value,s=document.getElementById('searchInput').value.toLowerCase();document.querySelectorAll('#logBody tr').forEach(function(r){var e=r.textContent.toLowerCase(),o=r.querySelector('.badge'),l=o?o.textContent.toLowerCase():'',n=!0;t!=='all'&&!l.includes(t)&&(n=!1);s&&!e.includes(s)&&(n=!1);r.style.display=n?'':'none'})}
</script>
</body>
</html>`

	type LogEntry struct {
		Timestamp  time.Time
		Type       string
		Details    string
		AdminName  string
		BadgeClass string
	}

	var logs []LogEntry

	for _, log := range storage.GetPromotionLogs() {
		logs = append(logs, LogEntry{
			Timestamp:  log.Timestamp,
			Type:       "promotion",
			Details:    fmt.Sprintf("%s → %s", log.UserName, log.NewRole),
			AdminName:  log.PromotedByName,
			BadgeClass: "badge-promotion",
		})
	}

	for _, log := range storage.GetBanLogs() {
		badgeClass := "badge-other"
		logType := "other"
		if log.Action == "ban" {
			badgeClass = "badge-ban"
			logType = "ban"
		} else if log.Action == "mute" {
			badgeClass = "badge-mute"
			logType = "mute"
		}
		logs = append(logs, LogEntry{
			Timestamp:  log.Timestamp,
			Type:       logType,
			Details:    fmt.Sprintf("%s: %s", log.UserName, log.Reason),
			AdminName:  log.AdminName,
			BadgeClass: badgeClass,
		})
	}

	for i := 0; i < len(logs)-1; i++ {
		for j := i + 1; j < len(logs); j++ {
			if logs[i].Timestamp.Before(logs[j].Timestamp) {
				logs[i], logs[j] = logs[j], logs[i]
			}
		}
	}

	t := template.Must(template.New("logs").Parse(tmpl))
	t.Execute(w, map[string]interface{}{"Logs": logs})
}

func handleWebConsole(w http.ResponseWriter, r *http.Request) {
	userID := getUserIDFromSession(r)
	user, _ := storage.GetUser(userID)

	if user == nil || user.RoleID != "owner" {
		http.Error(w, "Доступ запрещен. Только для владельца", http.StatusForbidden)
		return
	}

	tmpl := `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>🌿 Relax - Консоль</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#1a202c;color:#e2e8f0}
.header{background:#2d3748;padding:20px 40px;display:flex;justify-content:space-between;align-items:center}
.header h1{color:#48bb78;font-size:24px}.header a{color:#a0aec0;text-decoration:none}
.container{padding:40px;max-width:1400px;margin:0 auto;display:grid;grid-template-columns:1fr 1fr;gap:30px}
.panel{background:#2d3748;border-radius:12px;padding:24px;border:1px solid #4a5568}
.panel h2{color:#48bb78;font-size:18px;margin-bottom:16px;border-bottom:1px solid #4a5568;padding-bottom:12px}
.user-list{max-height:500px;overflow-y:auto}
.user-item{padding:12px;border-bottom:1px solid #4a5568;display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px}
.user-item:hover{background:#4a5568}
.user-name{font-weight:600}.user-role{color:#a0aec0;font-size:14px}
.actions{display:flex;gap:8px;flex-wrap:wrap}
.btn{padding:6px 14px;border:none;border-radius:6px;cursor:pointer;font-size:13px;font-weight:600;transition:all 0.2s}
.btn:hover{transform:scale(1.05)}
.btn-ban{background:#fc8181;color:#1a202c}
.btn-mute{background:#ed8936;color:#1a202c}
.btn-promote{background:#48bb78;color:#1a202c}
.btn-unban{background:#68d391;color:#1a202c}
.btn-unmute{background:#f6ad55;color:#1a202c}
.btn-danger{background:#f56565;color:white}
.form-group{margin-bottom:16px}
.form-group label{display:block;color:#a0aec0;margin-bottom:4px;font-size:14px}
.form-group input,.form-group select{width:100%;padding:10px 12px;background:#1a202c;border:1px solid #4a5568;border-radius:6px;color:#e2e8f0;font-size:14px}
.form-group input:focus,.form-group select:focus{outline:none;border-color:#48bb78}
.btn-submit{width:100%;padding:12px;background:#48bb78;color:#1a202c;border:none;border-radius:6px;font-weight:600;cursor:pointer;font-size:16px}
.btn-submit:hover{background:#38a169}
.status{padding:12px;border-radius:6px;margin-bottom:16px}
.status-success{background:#276749;color:#48bb78}
.status-error{background:#742a2a;color:#fc8181}
.roles-section{margin-top:16px}
.role-item{padding:8px 12px;background:#1a202c;border-radius:6px;margin-bottom:8px;display:flex;justify-content:space-between;align-items:center}
.role-item .role-prefix{font-size:20px}
.role-item .role-name{font-weight:600}
.role-item .role-level{color:#a0aec0;font-size:14px}
</style>
</head>
<body>
<div class="header"><h1>🖥️ Консоль</h1><a href="/dashboard">← Назад</a></div>
<div class="container">
<div class="panel">
<h2>👥 Пользователи</h2>
<div class="user-list">
{{range .Users}}
<div class="user-item">
<div><span>{{.Prefix}}</span> <span class="user-name">{{.FirstName}}</span> <span class="user-role">({{.RoleName}})</span>
{{if .IsBanned}}<span style="color:#fc8181;">🔴 Забанен</span>{{end}}
{{if .IsMuted}}<span style="color:#ed8936;">🔇 Замучен</span>{{end}}</div>
<div class="actions">
<button class="btn btn-promote" onclick="promoteUser({{.ID}})">⬆</button>
{{if .IsBanned}}<button class="btn btn-unban" onclick="unbanUser({{.ID}})">🔓</button>{{else}}<button class="btn btn-ban" onclick="banUser({{.ID}})">🔨</button>{{end}}
{{if .IsMuted}}<button class="btn btn-unmute" onclick="unmuteUser({{.ID}})">🔊</button>{{else}}<button class="btn btn-mute" onclick="muteUser({{.ID}})">🔇</button>{{end}}
</div></div>
{{end}}
</div></div>
<div>
<div class="panel">
<h2>⚡ Действия</h2>
<div id="statusMessages"></div>
<div class="form-group"><label>Пользователь</label>
<select id="actionUser">{{range .Users}}<option value="{{.ID}}">{{.Prefix}} {{.FirstName}}</option>{{end}}</select></div>
<div class="form-group"><label>Действие</label>
<select id="actionType" onchange="toggleActionFields()">
<option value="promote">Повысить</option>
<option value="demote">Понизить</option>
<option value="ban">Забанить</option>
<option value="mute">Замутить</option>
<option value="unban">Разбанить</option>
<option value="unmute">Размутить</option>
</select></div>
<div id="roleFields" style="display:none;">
<div class="form-group"><label>Новая роль</label>
<select id="newRole">{{range .Roles}}<option value="{{.ID}}">{{.Prefix}} {{.Name}}</option>{{end}}</select></div>
<div class="form-group"><label>Префикс</label><input type="text" id="newPrefix" placeholder="#Префикс"></div>
</div>
<div id="durationFields" style="display:none;">
<div class="form-group"><label>Длительность (мин)</label><input type="number" id="duration" value="60"></div>
</div>
<div class="form-group"><label>Причина</label><input type="text" id="reason" placeholder="Причина"></div>
<button class="btn-submit" onclick="executeAction()">▶ Выполнить</button>
</div>
<div class="panel" style="margin-top:20px;">
<h2>👑 Роли</h2>
<div class="roles-section">{{range .Roles}}
<div class="role-item"><div><span class="role-prefix">{{.Prefix}}</span> <span class="role-name">{{.Name}}</span> <span class="role-level">({{.Level}})</span></div>
<button class="btn btn-danger" onclick="deleteRole('{{.ID}}')">🗑️</button></div>
{{end}}</div>
<div style="margin-top:16px;"><h3 style="color:#a0aec0;font-size:14px;margin-bottom:8px;">Новая роль</h3>
<div class="form-group"><label>Название</label><input type="text" id="newRoleName" placeholder="Модератор"></div>
<div class="form-group"><label>Префикс</label><input type="text" id="newRolePrefix" placeholder="🛡️"></div>
<div class="form-group"><label>Уровень</label><input type="number" id="newRoleLevel" value="40"></div>
<button class="btn-submit" onclick="createRole()">➕ Создать</button>
</div></div></div></div>
<script>
function toggleActionFields(){var t=document.getElementById('actionType').value;document.getElementById('roleFields').style.display=t==='promote'||t==='demote'?'block':'none';document.getElementById('durationFields').style.display=t==='ban'||t==='mute'?'block':'none'}
function showStatus(t,e){document.getElementById('statusMessages').innerHTML='<div class="status status-'+e+'">'+t+'</div>';setTimeout(function(){document.getElementById('statusMessages').innerHTML=''},5000)}
function executeAction(){var t=document.getElementById('actionUser').value,e=document.getElementById('actionType').value,n=document.getElementById('reason').value||'Не указана',r=document.getElementById('duration')?document.getElementById('duration').value:60,o=document.getElementById('newRole')?document.getElementById('newRole').value:'',l=document.getElementById('newPrefix')?document.getElementById('newPrefix').value:'';if(!t){showStatus('Выберите пользователя','error');return}var a='/api/'+e;fetch(a,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({user_id:parseInt(t),reason:n,duration:parseInt(r),role_id:o,prefix:l})}).then(function(t){return t.json()}).then(function(t){if(t.success){showStatus('✅ '+t.message,'success');setTimeout(function(){window.location.reload()},1500)}else{showStatus('❌ '+t.message,'error')}}).catch(function(t){showStatus('❌ '+t,'error')})}
function promoteUser(t){document.getElementById('actionUser').value=t;document.getElementById('actionType').value='promote';toggleActionFields();document.getElementById('reason').value='Повышение за работу';executeAction()}
function banUser(t){document.getElementById('actionUser').value=t;document.getElementById('actionType').value='ban';toggleActionFields();document.getElementById('reason').value='Нарушение правил';executeAction()}
function unbanUser(t){document.getElementById('actionUser').value=t;document.getElementById('actionType').value='unban';toggleActionFields();document.getElementById('reason').value='Разбан';executeAction()}
function muteUser(t){document.getElementById('actionUser').value=t;document.getElementById('actionType').value='mute';toggleActionFields();document.getElementById('reason').value='Нарушение';executeAction()}
function unmuteUser(t){document.getElementById('actionUser').value=t;document.getElementById('actionType').value='unmute';toggleActionFields();document.getElementById('reason').value='Размут';executeAction()}
function createRole(){var t=document.getElementById('newRoleName').value,e=document.getElementById('newRolePrefix').value,n=parseInt(document.getElementById('newRoleLevel').value);if(!t){showStatus('Введите название','error');return}fetch('/api/roles',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:t,prefix:e,level:n})}).then(function(t){return t.json()}).then(function(t){if(t.success){showStatus('✅ Роль создана','success');setTimeout(function(){window.location.reload()},1500)}else{showStatus('❌ '+t.message,'error')}})}
function deleteRole(t){if(!confirm('Удалить роль?'))return;fetch('/api/roles/'+t,{method:'DELETE'}).then(function(t){return t.json()}).then(function(t){if(t.success){showStatus('✅ Роль удалена','success');setTimeout(function(){window.location.reload()},1500)}else{showStatus('❌ '+t.message,'error')}})}
toggleActionFields()
</script>
</body>
</html>`

	var users []map[string]interface{}
	for _, u := range storage.GetAllUsers() {
		role, _ := storage.GetRole(u.RoleID)
		roleName := "Пользователь"
		if role != nil {
			roleName = role.Name
		}
		users = append(users, map[string]interface{}{
			"ID":        u.ID,
			"FirstName": u.FirstName,
			"Prefix":    u.Prefix,
			"RoleName":  roleName,
			"IsBanned":  u.IsBanned,
			"IsMuted":   u.IsMuted,
		})
	}

	data := map[string]interface{}{
		"Users": users,
		"Roles": storage.GetAllRoles(),
	}

	t := template.Must(template.New("console").Parse(tmpl))
	t.Execute(w, data)
}

// ============================================
// API ОБРАБОТЧИКИ
// ============================================

func handleAPILogs(w http.ResponseWriter, r *http.Request) {
	userID := getUserIDFromSession(r)
	user, _ := storage.GetUser(userID)

	role, _ := storage.GetRole(user.RoleID)
	if role == nil || !role.CanSeeLogs {
		http.Error(w, "Доступ запрещен", http.StatusForbidden)
		return
	}

	var logs []map[string]interface{}
	for _, log := range storage.GetPromotionLogs() {
		logs = append(logs, map[string]interface{}{
			"timestamp": log.Timestamp,
			"type":      "promotion",
			"details":   fmt.Sprintf("%s → %s", log.UserName, log.NewRole),
			"admin":     log.PromotedByName,
		})
	}
	json.NewEncoder(w).Encode(logs)
}

func handleAPIUsers(w http.ResponseWriter, r *http.Request) {
	userID := getUserIDFromSession(r)
	user, _ := storage.GetUser(userID)

	if user == nil || user.RoleID != "owner" {
		http.Error(w, "Доступ запрещен", http.StatusForbidden)
		return
	}

	var users []map[string]interface{}
	for _, u := range storage.GetAllUsers() {
		role, _ := storage.GetRole(u.RoleID)
		roleName := ""
		if role != nil {
			roleName = role.Name
		}
		users = append(users, map[string]interface{}{
			"id":         u.ID,
			"first_name": u.FirstName,
			"role":       roleName,
			"is_banned":  u.IsBanned,
			"is_muted":   u.IsMuted,
		})
	}
	json.NewEncoder(w).Encode(users)
}

func handleAPIBan(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Метод не разрешен", http.StatusMethodNotAllowed)
		return
	}

	userID := getUserIDFromSession(r)
	admin, _ := storage.GetUser(userID)

	if admin == nil || admin.RoleID != "owner" {
		http.Error(w, "Доступ запрещен", http.StatusForbidden)
		return
	}

	var req struct {
		UserID   int64  `json:"user_id"`
		Reason   string `json:"reason"`
		Duration int    `json:"duration"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Ошибка запроса"})
		return
	}

	user, err := storage.GetUser(req.UserID)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Пользователь не найден"})
		return
	}

	user.IsBanned = true
	user.BanReason = req.Reason
	storage.UpdateUser(user)

	storage.AddBanLog(&BanLog{
		Timestamp: time.Now(),
		UserID:    req.UserID,
		UserName:  user.FirstName,
		Action:    "ban",
		Duration:  fmt.Sprintf("%d мин", req.Duration),
		Reason:    req.Reason,
		AdminID:   admin.ID,
		AdminName: admin.FirstName,
	})

	if config.LogsGroupID != 0 {
		msg := fmt.Sprintf("🔨 БАН\n\n👤 %s\n📝 %s\n👑 %s", user.FirstName, req.Reason, admin.FirstName)
		bot.Send(tgbotapi.NewMessage(config.LogsGroupID, msg))
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Пользователь забанен"})
}

func handleAPIMute(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Метод не разрешен", http.StatusMethodNotAllowed)
		return
	}

	userID := getUserIDFromSession(r)
	admin, _ := storage.GetUser(userID)

	if admin == nil || admin.RoleID != "owner" {
		http.Error(w, "Доступ запрещен", http.StatusForbidden)
		return
	}

	var req struct {
		UserID   int64  `json:"user_id"`
		Reason   string `json:"reason"`
		Duration int    `json:"duration"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Ошибка запроса"})
		return
	}

	user, err := storage.GetUser(req.UserID)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Пользователь не найден"})
		return
	}

	muteUntil := time.Now().Add(time.Duration(req.Duration) * time.Minute)
	user.IsMuted = true
	user.MuteUntil = &muteUntil
	storage.UpdateUser(user)

	storage.AddBanLog(&BanLog{
		Timestamp: time.Now(),
		UserID:    req.UserID,
		UserName:  user.FirstName,
		Action:    "mute",
		Duration:  fmt.Sprintf("%d мин", req.Duration),
		Reason:    req.Reason,
		AdminID:   admin.ID,
		AdminName: admin.FirstName,
	})

	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Пользователь замучен"})
}

func handleAPIPromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Метод не разрешен", http.StatusMethodNotAllowed)
		return
	}

	userID := getUserIDFromSession(r)
	admin, _ := storage.GetUser(userID)

	if admin == nil || admin.RoleID != "owner" {
		http.Error(w, "Доступ запрещен", http.StatusForbidden)
		return
	}

	var req struct {
		UserID int64  `json:"user_id"`
		RoleID string `json:"role_id"`
		Prefix string `json:"prefix"`
		Reason string `json:"reason"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Ошибка запроса"})
		return
	}

	user, err := storage.GetUser(req.UserID)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Пользователь не найден"})
		return
	}

	oldRole := user.RoleID
	oldPrefix := user.Prefix
	user.RoleID = req.RoleID
	if req.Prefix != "" {
		user.Prefix = req.Prefix
	}
	storage.UpdateUser(user)

	role, _ := storage.GetRole(req.RoleID)
	storage.AddPromotionLog(&PromotionLog{
		Timestamp:      time.Now(),
		UserID:         req.UserID,
		UserName:       user.FirstName,
		OldRole:        oldRole,
		NewRole:        role.Name,
		OldPrefix:      oldPrefix,
		NewPrefix:      user.Prefix,
		Reason:         req.Reason,
		PromotedBy:     admin.ID,
		PromotedByName: admin.FirstName,
		Action:         "promotion",
	})

	if config.PromotionsGroupID != 0 {
		msg := fmt.Sprintf("🎉 ПОВЫШЕНИЕ\n\n👤 %s\n📈 %s\n🔑 %s\n📝 %s\n👑 %s",
			user.FirstName, role.Name, user.Prefix, req.Reason, admin.FirstName)
		bot.Send(tgbotapi.NewMessage(config.PromotionsGroupID, msg))
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Пользователь повышен"})
}

func handleAPIRoles(w http.ResponseWriter, r *http.Request) {
	userID := getUserIDFromSession(r)
	user, _ := storage.GetUser(userID)

	if user == nil || user.RoleID != "owner" {
		http.Error(w, "Доступ запрещен", http.StatusForbidden)
		return
	}

	if r.Method == "GET" {
		json.NewEncoder(w).Encode(storage.GetAllRoles())
		return
	}

	if r.Method == "POST" {
		var req struct {
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
			Level  int    `json:"level"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "Ошибка"})
			return
		}

		role := &Role{
			ID:             uuid.New().String(),
			Name:           req.Name,
			Prefix:         req.Prefix,
			Level:          req.Level,
			Permissions:    []string{},
			CanSeeLogs:     false,
			CanManageRoles: false,
			CanBan:         false,
			CanMute:        false,
			CreatedAt:      time.Now(),
			CreatedBy:      user.ID,
		}
		storage.SaveRole(role)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "role": role})
		return
	}

	if r.Method == "DELETE" {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 3 {
			http.Error(w, "Неверный запрос", http.StatusBadRequest)
			return
		}
		storage.DeleteRole(parts[2])
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
		return
	}
}

// ============================================
// ВСПОМОГАТЕЛЬНЫЕ
// ============================================

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_id")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		_, err = storage.GetSession(cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func getUserIDFromSession(r *http.Request) int64 {
	cookie, err := r.Cookie("session_id")
	if err != nil {
		return 0
	}
	session, err := storage.GetSession(cookie.Value)
	if err != nil {
		return 0
	}
	return session.UserID
}

func generateSessionID() string {
	return uuid.New().String()
}

// ============================================
// БОТ - ОБРАБОТЧИКИ
// ============================================

func handleUpdate(update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		handleCallback(update.CallbackQuery)
		return
	}
	if update.Message != nil {
		handleMessage(update.Message)
		return
	}
}

func handleMessage(msg *tgbotapi.Message) {
	userID := msg.From.ID

	user, err := storage.GetUser(userID)
	if err == nil && user.IsBanned {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Вы забанены. Причина: "+user.BanReason))
		return
	}
	if err == nil && user.IsMuted {
		if user.MuteUntil != nil && user.MuteUntil.After(time.Now()) {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "🔇 Вы замучены до "+user.MuteUntil.Format("15:04 02.01.2006")))
			return
		} else {
			user.IsMuted = false
			user.MuteUntil = nil
			storage.UpdateUser(user)
		}
	}

	text := msg.Text

	if strings.HasPrefix(text, "/ask ") && isAdmin(userID) {
		handleAIRequest(msg)
		return
	}
	if text == "/profile" {
		handleProfile(msg)
		return
	}
	if text == "/start" {
		handleStart(msg)
		return
	}

	state := storage.GetUserState(userID)

	if state.Step == "chatting" {
		handleChatMessage(msg)
		return
	}
	if state.Step == "waiting_technical" {
		handleTechnicalMessage(msg)
		return
	}
	if state.Step == "waiting_message" {
		handleSupportMessage(msg)
		return
	}
	if state.Step == "writing_review" {
		handleReviewMessage(msg)
		return
	}

	switch text {
	case "💬 Написать администратору":
		handleStartSupport(msg)
	case "📞 Техническая поддержка":
		handleStartTechnical(msg)
	case "📂 Мои обращения":
		handleMyTickets(msg)
	case "ℹ️ Информация":
		handleInfo(msg)
	case "👤 Мой профиль":
		handleProfile(msg)
	case "📋 Активные обращения":
		if isAdmin(userID) {
			handleActiveTickets(msg)
		}
	case "👥 Команда":
		if isAdmin(userID) {
			handleTeamList(msg)
		}
	case "📊 Статистика":
		if isAdmin(userID) {
			handleStatistics(msg)
		}
	case "🧑‍💻 Админ панель":
		if isAdmin(userID) {
			handleAdminPanel(msg)
		}
	case "📜 История действий":
		if isAdmin(userID) {
			handleLogs(msg)
		}
	case "⚙️ Настройки":
		if isOwner(userID) {
			handleSettings(msg)
		}
	case "👑 Управление ролями":
		if isOwner(userID) {
			handleManageRoles(msg)
		}
	case "🔙 Назад":
		handleBack(msg)
	default:
		handleUnknown(msg)
	}
}

func handleCallback(callback *tgbotapi.CallbackQuery) {
	data := callback.Data

	switch {
	case strings.HasPrefix(data, "take_"):
		handleTakeTicket(callback)
	case strings.HasPrefix(data, "skip_"):
		handleSkipTicket(callback)
	case strings.HasPrefix(data, "close_"):
		handleCloseTicket(callback)
	case strings.HasPrefix(data, "rate_"):
		handleRateTicket(callback)
	case strings.HasPrefix(data, "tech_approve_"):
		handleApproveTechnical(callback)
	case strings.HasPrefix(data, "tech_reject_"):
		handleRejectTechnical(callback)
	case strings.HasPrefix(data, "tech_open_"):
		handleOpenTechnical(callback)
	case data == "admin_panel_tickets":
		handleAdminTicketsList(callback)
	case data == "admin_panel_team":
		handleAdminTeamList(callback)
	case data == "admin_panel_stats":
		handleAdminStats(callback)
	case data == "admin_panel_profile":
		handleAdminProfile(callback)
	default:
		callback.Answer()
	}
}

// ============================================
// БОТ - ПОЛЬЗОВАТЕЛЬСКИЕ ОБРАБОТЧИКИ
// ============================================

func handleStart(msg *tgbotapi.Message) {
	userID := msg.From.ID
	firstName := msg.From.FirstName

	_, err := storage.GetUser(userID)
	if err != nil {
		user := &User{
			ID:           userID,
			Username:     msg.From.UserName,
			FirstName:    firstName,
			LastName:     msg.From.LastName,
			RoleID:       "",
			Prefix:       "",
			RegisteredAt: time.Now(),
			IsActive:     true,
			Reputation:   0,
			DaysInTeam:   0,
			TicketsCount: 0,
			Rating:       0,
		}
		storage.SaveUser(user)
	}

	storage.ClearUserState(userID)

	keyboard := GetMainKeyboard()
	if isAdmin(userID) {
		keyboard = GetAdminKeyboard()
	}
	if isOwner(userID) {
		keyboard = GetOwnerKeyboard()
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(
		"🌿 Relax\n\nПривет, %s! 👋\nДобро пожаловать в бот поддержки Relax.\n\nНаши администраторы готовы помочь тебе 💚",
		firstName,
	))
	reply.ReplyMarkup = keyboard
	bot.Send(reply)
}

func handleStartSupport(msg *tgbotapi.Message) {
	userID := msg.From.ID
	state := &UserState{Step: "waiting_message"}
	storage.SetUserState(userID, state)
	reply := tgbotapi.NewMessage(msg.Chat.ID, "💬 Опишите вашу проблему или вопрос:")
	reply.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	bot.Send(reply)
}

func handleSupportMessage(msg *tgbotapi.Message) {
	userID := msg.From.ID
	text := msg.Text

	ticketID := storage.GenerateTicketID()
	user, _ := storage.GetUser(userID)

	ticket := &Ticket{
		ID:        ticketID,
		UserID:    userID,
		User:      user.FirstName,
		Type:      "support",
		Status:    "waiting",
		Message:   text,
		CreatedAt: time.Now(),
		IsActive:  true,
	}
	storage.SaveTicket(ticket)

	adminKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Взять обращение", "take_"+ticketID),
			tgbotapi.NewInlineKeyboardButtonData("❌ Пропустить", "skip_"+ticketID),
		),
	)

	admins := storage.GetAllAdmins()
	for _, admin := range admins {
		adminMsg := tgbotapi.NewMessage(admin.ID, fmt.Sprintf(
			"🌿 Relax Panel\n\nНовое обращение!\n\n👤 Пользователь: %s\n📌 Тип: Поддержка\n💬 Сообщение: %s\n\n🟡 Статус: Ожидание\n🎫 #%s",
			user.FirstName, text, ticketID,
		))
		adminMsg.ReplyMarkup = adminKeyboard
		bot.Send(adminMsg)
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(
		"✅ Ваше обращение принято.\n\nОжидайте ответа 💚\n🎫 #%s", ticketID,
	))
	reply.ReplyMarkup = GetMainKeyboard()
	bot.Send(reply)
	storage.ClearUserState(userID)
}

func handleStartTechnical(msg *tgbotapi.Message) {
	userID := msg.From.ID
	state := &UserState{Step: "waiting_technical"}
	storage.SetUserState(userID, state)
	reply := tgbotapi.NewMessage(msg.Chat.ID, "📞 Выберите категорию:")
	reply.ReplyMarkup = GetTechnicalCategoriesKeyboard()
	bot.Send(reply)
}

func handleTechnicalMessage(msg *tgbotapi.Message) {
	userID := msg.From.ID
	text := msg.Text
	state := storage.GetUserState(userID)

	if text == "🔙 Назад" {
		storage.ClearUserState(userID)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Возврат.")
		reply.ReplyMarkup = GetMainKeyboard()
		bot.Send(reply)
		return
	}

	techID := storage.GenerateTechnicalID()
	user, _ := storage.GetUser(userID)

	techReq := &TechnicalRequest{
		ID:          techID,
		UserID:      userID,
		Username:    user.FirstName,
		Category:    state.TechnicalType,
		Description: text,
		Status:      "pending",
		CreatedAt:   time.Now(),
	}
	storage.SaveTechnicalRequest(techReq)

	techKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👀 Открыть", "tech_open_"+techID),
			tgbotapi.NewInlineKeyboardButtonData("✅ Одобрить", "tech_approve_"+techID),
			tgbotapi.NewInlineKeyboardButtonData("❌ Отклонить", "tech_reject_"+techID),
		),
	)

	for _, admin := range storage.GetAllAdmins() {
		role, _ := storage.GetRole(admin.RoleID)
		if role != nil && role.Name == "Технический администратор" {
			adminMsg := tgbotapi.NewMessage(admin.ID, fmt.Sprintf(
				"🌿 Relax Technical Panel\n\nНовая заявка!\n📞 #%s\nТип: %s\nОт: %s\nОписание: %s",
				techID, state.TechnicalType, user.FirstName, text,
			))
			adminMsg.ReplyMarkup = techKeyboard
			bot.Send(adminMsg)
		}
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(
		"✅ Заявка #%s принята.\nТип: %s\nСтатус: Рассмотрение", techID, state.TechnicalType,
	))
	reply.ReplyMarkup = GetMainKeyboard()
	bot.Send(reply)
	storage.ClearUserState(userID)
}

func handleMyTickets(msg *tgbotapi.Message) {
	userID := msg.From.ID
	tickets := storage.GetUserTickets(userID)
	if len(tickets) == 0 {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "📂 У вас нет обращений.")
		reply.ReplyMarkup = GetMainKeyboard()
		bot.Send(reply)
		return
	}
	var text strings.Builder
	text.WriteString("📂 Ваши обращения:\n\n")
	for _, ticket := range tickets {
		status := "🟡 В обработке"
		if ticket.Status == "closed" {
			status = "✅ Закрыто"
		}
		text.WriteString(fmt.Sprintf("🎫 #%s - %s\n", ticket.ID, status))
	}
	reply := tgbotapi.NewMessage(msg.Chat.ID, text.String())
	reply.ReplyMarkup = GetMainKeyboard()
	bot.Send(reply)
}

func handleInfo(msg *tgbotapi.Message) {
	reply := tgbotapi.NewMessage(msg.Chat.ID,
		"🌿 Relax\n\nВерсия: 2.0.0\nБот поддержки с веб-панелью.\n\n💚 Поддержка, которая рядом")
	reply.ReplyMarkup = GetMainKeyboard()
	bot.Send(reply)
}

func handleProfile(msg *tgbotapi.Message) {
	userID := msg.From.ID
	user, err := storage.GetUser(userID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Профиль не найден."))
		return
	}
	role, _ := storage.GetRole(user.RoleID)
	roleName := "Пользователь"
	if role != nil {
		roleName = role.Name
	}
	text := fmt.Sprintf(
		"🌿 Relax Profile\n\n%s %s\n\n🛡 Ранг: %s\n⭐ Репутация: %d\n📅 Дней: %d\n🎫 Обращений: %d\n⭐ Оценка: %.1f/5",
		user.Prefix, user.FirstName, roleName, user.Reputation, user.DaysInTeam, user.TicketsCount, user.Rating,
	)
	reply := tgbotapi.NewMessage(msg.Chat.ID, text)
	if user.RoleID == "owner" {
		reply.ReplyMarkup = GetOwnerKeyboard()
	} else if user.RoleID != "" {
		reply.ReplyMarkup = GetAdminKeyboard()
	} else {
		reply.ReplyMarkup = GetMainKeyboard()
	}
	bot.Send(reply)
}

func handleReviewMessage(msg *tgbotapi.Message) {
	userID := msg.From.ID
	state := storage.GetUserState(userID)

	if state.Step != "writing_review" {
		return
	}

	ticketID := state.TicketID
	rating, _ := strconv.Atoi(state.TempMessage)
	reviewText := msg.Text

	ticket, err := storage.GetTicket(ticketID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Ошибка"))
		storage.ClearUserState(userID)
		return
	}

	ticket.ReviewText = reviewText
	storage.UpdateTicket(ticket)

	admin, _ := storage.GetUser(ticket.AdminID)
	if admin != nil {
		total := float64(admin.Rating*float64(admin.TicketsCount) + float64(rating))
		admin.TicketsCount++
		admin.Rating = total / float64(admin.TicketsCount)
		admin.Reputation += rating
		storage.UpdateUser(admin)
	}

	if config.ReviewsGroupID != 0 {
		reviewMsg := fmt.Sprintf(
			"🌟 НОВЫЙ ОТЗЫВ\n\n👤 Пользователь: %s\n💬 Отзыв: \"%s\"\n⭐ Оценка: %d/5\n👨‍💼 Админ: %s\n🎫 #%s",
			ticket.User, reviewText, rating, ticket.AdminName, ticket.ID,
		)
		bot.Send(tgbotapi.NewMessage(config.ReviewsGroupID, reviewMsg))
	}

	bot.Send(tgbotapi.NewMessage(msg.Chat.ID,
		fmt.Sprintf("✅ Спасибо! Оценка: %d/5\nОтзыв: \"%s\"", rating, reviewText)))
	storage.ClearUserState(userID)
}

// ============================================
// БОТ - АДМИНСКИЕ ОБРАБОТЧИКИ
// ============================================

func handleTakeTicket(callback *tgbotapi.CallbackQuery) {
	ticketID := strings.TrimPrefix(callback.Data, "take_")
	adminID := callback.From.ID
	adminName := callback.From.FirstName

	ticket, err := storage.GetTicket(ticketID)
	if err != nil || ticket.Status != "waiting" {
		callback.Answer("Ошибка")
		return
	}

	ticket.AdminID = adminID
	ticket.AdminName = adminName
	ticket.Status = "in_progress"
	storage.UpdateTicket(ticket)

	admin, _ := storage.GetUser(adminID)
	if admin != nil {
		admin.TicketsCount++
		storage.UpdateUser(admin)
	}

	bot.Send(tgbotapi.NewMessage(ticket.UserID, fmt.Sprintf(
		"✅ Ваше обращение принято.\n\nАдминистратор: %s\nОжидайте ответа 💚", adminName,
	)))

	state := &UserState{Step: "chatting", TicketID: ticket.ID}
	storage.SetUserState(ticket.UserID, state)
	storage.SetUserState(adminID, state)

	callback.Answer("Вы взяли обращение")
	callback.Message.Text = "✅ Взято"
	bot.Send(callback.Message)
}

func handleSkipTicket(callback *tgbotapi.CallbackQuery) {
	ticketID := strings.TrimPrefix(callback.Data, "skip_")
	ticket, err := storage.GetTicket(ticketID)
	if err != nil {
		callback.Answer("Ошибка")
		return
	}
	ticket.Status = "waiting"
	ticket.AdminID = 0
	ticket.AdminName = ""
	storage.UpdateTicket(ticket)

	bot.Send(tgbotapi.NewMessage(ticket.UserID,
		"🌿 Мы уже ищем свободного администратора.\nПожалуйста, ожидайте 💚"))

	callback.Answer("Пропущено")
	callback.Message.Text = "❌ Пропущено"
	bot.Send(callback.Message)
}

func handleCloseTicket(callback *tgbotapi.CallbackQuery) {
	ticketID := strings.TrimPrefix(callback.Data, "close_")
	ticket, err := storage.GetTicket(ticketID)
	if err != nil {
		callback.Answer("Ошибка")
		return
	}

	now := time.Now()
	ticket.Status = "closed"
	ticket.ClosedAt = &now
	ticket.IsActive = false
	storage.UpdateTicket(ticket)

	storage.ClearUserState(ticket.UserID)
	storage.ClearUserState(ticket.AdminID)

	rateKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⭐ 1", "rate_1_"+ticketID),
			tgbotapi.NewInlineKeyboardButtonData("⭐ 2", "rate_2_"+ticketID),
			tgbotapi.NewInlineKeyboardButtonData("⭐ 3", "rate_3_"+ticketID),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⭐ 4", "rate_4_"+ticketID),
			tgbotapi.NewInlineKeyboardButtonData("⭐ 5", "rate_5_"+ticketID),
		),
	)

	userMsg := tgbotapi.NewMessage(ticket.UserID,
		"✅ Обращение закрыто.\n\nСпасибо! 💚\nОцените работу:")
	userMsg.ReplyMarkup = rateKeyboard
	bot.Send(userMsg)

	callback.Answer("Закрыто")
	callback.Message.Text = "🔒 Закрыто"
	bot.Send(callback.Message)
}

func handleRateTicket(callback *tgbotapi.CallbackQuery) {
	parts := strings.Split(callback.Data, "_")
	rating, _ := strconv.Atoi(parts[1])
	ticketID := parts[2]

	ticket, err := storage.GetTicket(ticketID)
	if err != nil {
		callback.Answer("Ошибка")
		return
	}

	callback.Answer("Спасибо! Напишите отзыв:")

	state := &UserState{
		Step:         "writing_review",
		TicketID:     ticketID,
		TempMessage:  strconv.Itoa(rating),
	}
	storage.SetUserState(callback.From.ID, state)

	ratingVal := rating
	ticket.Rating = &ratingVal
	storage.UpdateTicket(ticket)

	bot.Send(tgbotapi.NewMessage(callback.From.ID,
		"✍️ Напишите текстовый отзыв о работе администратора:"))
}

func handleChatMessage(msg *tgbotapi.Message) {
	userID := msg.From.ID
	state := storage.GetUserState(userID)
	text := msg.Text

	ticket, err := storage.GetTicket(state.TicketID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Ошибка"))
		return
	}

	if isAdmin(userID) {
		bot.Send(tgbotapi.NewMessage(ticket.UserID,
			fmt.Sprintf("💬 Сообщение от администратора:\n\n%s", text)))
	} else {
		if ticket.AdminID != 0 {
			closeKeyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🔒 Закрыть", "close_"+ticket.ID),
				),
			)
			adminMsg := tgbotapi.NewMessage(ticket.AdminID,
				fmt.Sprintf("💬 Сообщение от пользователя:\n\n%s", text))
			adminMsg.ReplyMarkup = closeKeyboard
			bot.Send(adminMsg)
		}
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Сообщение отправлено."))
	}
}

// ============================================
// БОТ - ВСПОМОГАТЕЛЬНЫЕ
// ============================================

func isAdmin(userID int64) bool {
	user, err := storage.GetUser(userID)
	if err != nil {
		return false
	}
	return user.RoleID != "" && user.IsActive
}

func isOwner(userID int64) bool {
	user, err := storage.GetUser(userID)
	if err != nil {
		return false
	}
	return user.RoleID == "owner"
}

func handleActiveTickets(msg *tgbotapi.Message) {
	tickets := storage.GetActiveTickets()
	if len(tickets) == 0 {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "📋 Нет активных обращений"))
		return
	}
	var text strings.Builder
	text.WriteString("📋 Активные:\n\n")
	for _, t := range tickets {
		status := "🟡 Ожидание"
		if t.Status == "in_progress" {
			status = "🟢 В работе (" + t.AdminName + ")"
		}
		text.WriteString(fmt.Sprintf("🎫 #%s - %s\n", t.ID, status))
	}
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, text.String()))
}

func handleTeamList(msg *tgbotapi.Message) {
	admins := storage.GetAllAdmins()
	if len(admins) == 0 {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "👥 Нет администраторов"))
		return
	}
	var text strings.Builder
	text.WriteString("👥 Команда:\n\n")
	for _, a := range admins {
		role, _ := storage.GetRole(a.RoleID)
		roleName := "Без роли"
		if role != nil {
			roleName = role.Name
		}
		text.WriteString(fmt.Sprintf("%s %s - %s\n", a.Prefix, a.FirstName, roleName))
	}
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, text.String()))
}

func handleStatistics(msg *tgbotapi.Message) {
	userID := msg.From.ID
	user, _ := storage.GetUser(userID)
	text := fmt.Sprintf(
		"📊 Статистика\n\n⭐ Репутация: %d\n🎫 Обращений: %d\n⭐ Оценка: %.1f/5",
		user.Reputation, user.TicketsCount, user.Rating,
	)
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, text))
}

func handleAdminPanel(msg *tgbotapi.Message) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🎫 Обращения", "admin_panel_tickets"),
			tgbotapi.NewInlineKeyboardButtonData("👥 Команда", "admin_panel_team"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Статистика", "admin_panel_stats"),
			tgbotapi.NewInlineKeyboardButtonData("👤 Профиль", "admin_panel_profile"),
		),
	)
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "🧑‍💻 Админ панель:", tgbotapi.WithReplyMarkup(keyboard)))
}

func handleAdminTicketsList(callback *tgbotapi.CallbackQuery) {
	tickets := storage.GetActiveTickets()
	if len(tickets) == 0 {
		callback.Answer("Нет активных")
		return
	}
	var text strings.Builder
	text.WriteString("🎫 Активные:\n\n")
	for _, t := range tickets {
		text.WriteString(fmt.Sprintf("#%s - %s\n", t.ID, t.User))
	}
	callback.Answer()
	bot.Send(tgbotapi.NewMessage(callback.From.ID, text.String()))
}

func handleAdminTeamList(callback *tgbotapi.CallbackQuery) {
	admins := storage.GetAllAdmins()
	var text strings.Builder
	text.WriteString("👥 Команда:\n\n")
	for _, a := range admins {
		role, _ := storage.GetRole(a.RoleID)
		roleName := "Без роли"
		if role != nil {
			roleName = role.Name
		}
		text.WriteString(fmt.Sprintf("%s %s - %s\n", a.Prefix, a.FirstName, roleName))
	}
	callback.Answer()
	bot.Send(tgbotapi.NewMessage(callback.From.ID, text.String()))
}

func handleAdminStats(callback *tgbotapi.CallbackQuery) {
	userID := callback.From.ID
	user, _ := storage.GetUser(userID)
	text := fmt.Sprintf("📊 Статистика\n\n⭐ Репутация: %d\n🎫 Обращений: %d\n⭐ Оценка: %.1f/5",
		user.Reputation, user.TicketsCount, user.Rating)
	callback.Answer()
	bot.Send(tgbotapi.NewMessage(callback.From.ID, text))
}

func handleAdminProfile(callback *tgbotapi.CallbackQuery) {
	userID := callback.From.ID
	user, _ := storage.GetUser(userID)
	role, _ := storage.GetRole(user.RoleID)
	roleName := "Пользователь"
	if role != nil {
		roleName = role.Name
	}
	text := fmt.Sprintf("👤 Профиль\n\nИмя: %s\nРанг: %s\nПрефикс: %s\n⭐ Репутация: %d",
		user.FirstName, roleName, user.Prefix, user.Reputation)
	callback.Answer()
	bot.Send(tgbotapi.NewMessage(callback.From.ID, text))
}

func handleLogs(msg *tgbotapi.Message) {
	logs := storage.GetPromotionLogs()
	if len(logs) == 0 {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "📜 Логов нет"))
		return
	}
	var text strings.Builder
	text.WriteString("📜 Последние логи:\n\n")
	count := 10
	if len(logs) < count {
		count = len(logs)
	}
	for i := len(logs) - count; i < len(logs); i++ {
		l := logs[i]
		text.WriteString(fmt.Sprintf("%s: %s\n", l.Timestamp.Format("15:04"), l.Action))
	}
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, text.String()))
}

func handleSettings(msg *tgbotapi.Message) {
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID,
		"⚙️ Настройки Relax\n\nДля изменения используйте веб-панель."))
}

func handleManageRoles(msg *tgbotapi.Message) {
	roles := storage.GetAllRoles()
	var text strings.Builder
	text.WriteString("👑 Роли:\n\n")
	for _, r := range roles {
		text.WriteString(fmt.Sprintf("%s %s (Уровень %d)\n", r.Prefix, r.Name, r.Level))
	}
	text.WriteString("\nУправляйте ролями в веб-панели.")
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, text.String()))
}

func handleBack(msg *tgbotapi.Message) {
	userID := msg.From.ID
	storage.ClearUserState(userID)
	keyboard := GetMainKeyboard()
	if isAdmin(userID) {
		keyboard = GetAdminKeyboard()
	}
	if isOwner(userID) {
		keyboard = GetOwnerKeyboard()
	}
	reply := tgbotapi.NewMessage(msg.Chat.ID, "🔙 Главное меню.")
	reply.ReplyMarkup = keyboard
	bot.Send(reply)
}

func handleUnknown(msg *tgbotapi.Message) {}

func handleAIRequest(msg *tgbotapi.Message) {
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID,
		"🤖 Relax AI\n\nВозможный ответ:\nЗдравствуйте! Опишите подробнее вашу проблему."))
}

func handleApproveTechnical(callback *tgbotapi.CallbackQuery) {
	techID := strings.TrimPrefix(callback.Data, "tech_approve_")
	req, err := storage.GetTechnicalRequest(techID)
	if err != nil {
		callback.Answer("Ошибка")
		return
	}
	now := time.Now()
	req.Status = "approved"
	req.ReviewedAt = &now
	req.ReviewedBy = callback.From.ID
	storage.SaveTechnicalRequest(req)

	bot.Send(tgbotapi.NewMessage(req.UserID, fmt.Sprintf(
		"✅ Заявка #%s одобрена! 💚", techID)))
	callback.Answer("Одобрено")
	callback.Message.Text = "✅ Одобрено"
	bot.Send(callback.Message)
}

func handleRejectTechnical(callback *tgbotapi.CallbackQuery) {
	techID := strings.TrimPrefix(callback.Data, "tech_reject_")
	req, err := storage.GetTechnicalRequest(techID)
	if err != nil {
		callback.Answer("Ошибка")
		return
	}
	now := time.Now()
	req.Status = "rejected"
	req.ReviewedAt = &now
	req.ReviewedBy = callback.From.ID
	storage.SaveTechnicalRequest(req)

	bot.Send(tgbotapi.NewMessage(req.UserID, fmt.Sprintf(
		"❌ Заявка #%s отклонена.", techID)))
	callback.Answer("Отклонено")
	callback.Message.Text = "❌ Отклонено"
	bot.Send(callback.Message)
}

func handleOpenTechnical(callback *tgbotapi.CallbackQuery) {
	techID := strings.TrimPrefix(callback.Data, "tech_open_")
	req, err := storage.GetTechnicalRequest(techID)
	if err != nil {
		callback.Answer("Ошибка")
		return
	}
	text := fmt.Sprintf(
		"📞 Заявка #%s\n\nТип: %s\nОт: %s\nОписание: %s\nСтатус: %s",
		techID, req.Category, req.Username, req.Description, req.Status,
	)
	callback.Answer()
	bot.Send(tgbotapi.NewMessage(callback.From.ID, text))
}

// ============================================
// ЗАПУСК
// ============================================

func init() {
	// Проверяем наличие переменных
	if config.Token == "" {
		log.Println("⚠️ TELEGRAM_TOKEN не задан!")
	}
}