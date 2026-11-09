package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
)

var db *sql.DB

// ส่ง JSON กลับไปยัง Client
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// ตรวจสอบ Backend
func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "StudyShare API is running!",
		"status":  "success",
	})
}

// ตรวจสอบ Database
func dbTestHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":  "error",
			"message": "Database connection failed",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Connected to Supabase successfully!",
	})
}

type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	// จำกัดขนาดข้อมูลที่ส่งเข้ามา
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req RegisterRequest

	// อ่าน JSON
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON data",
		})
		return
	}

	// ตรวจสอบข้อมูล
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)

	if req.Username == "" || req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Username, email and password are required",
		})
		return
	}

	if len(req.Username) > 50 || len(req.Email) > 254 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Username or email is too long",
		})
		return
	}

	if len([]byte(req.Password)) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Password must be at least 8 bytes",
		})
		return
	}

	// bcrypt รองรับรหัสผ่านได้ไม่เกิน 72 bytes
	if len([]byte(req.Password)) > 72 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Password must not exceed 72 bytes",
		})
		return
	}

	// Hash password ก่อนบันทึก
	hashedPassword, err := bcrypt.GenerateFromPassword(
		[]byte(req.Password),
		bcrypt.DefaultCost,
	)

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot hash password",
		})
		return
	}

	// บันทึกสมาชิกลงฐานข้อมูล
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id int
	var username, email, role string
	var createdAt time.Time

	err = db.QueryRowContext(ctx, `
		INSERT INTO app_users
			(username, email, password_hash, role, created_at)
		VALUES ($1, $2, $3, 'member', NOW())
		RETURNING id, username, email, role, created_at
	`,
		req.Username,
		req.Email,
		string(hashedPassword),
	).Scan(&id, &username, &email, &role, &createdAt)

	if err != nil {
		// ตรวจสอบข้อมูลซ้ำจาก UNIQUE constraint
		var pgErr *pgconn.PgError

		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Username or email already exists",
			})
			return
		}

		log.Println("Register error:", err)

		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot register user",
		})
		return
	}

	// ส่งข้อมูลกลับโดยไม่เปิดเผย Password Hash
	writeJSON(w, http.StatusCreated, map[string]any{
		"message": "Registration successful",
		"user": map[string]any{
			"id":         id,
			"username":   username,
			"email":      email,
			"role":       role,
			"created_at": createdAt,
		},
	})
}

// API ดึงรายวิชาทั้งหมด
func subjectsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx,
		`SELECT id, subject_name
		 FROM subjects
		 ORDER BY id`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot get subjects",
		})
		return
	}
	defer rows.Close()

	subjects := []map[string]any{}

	for rows.Next() {
		var id int
		var name string

		if err := rows.Scan(&id, &name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Cannot read subject data",
			})
			return
		}

		subjects = append(subjects, map[string]any{
			"id":           id,
			"subject_name": name,
		})
	}

	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot read subjects",
		})
		return
	}

	writeJSON(w, http.StatusOK, subjects)
}

// API ดึงสรุปบทเรียนทั้งหมด
func summariesHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	q := r.URL.Query().Get("q")

	var subjectID any
	if value := r.URL.Query().Get("subject_id"); value != "" {
		id, err := strconv.Atoi(value)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Invalid subject_id",
			})
			return
		}
		subjectID = id
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			s.id,
			s.title,
			s.content,
			s.subject_id,
			s.user_id,
			s.created_at,
			s.updated_at,
			sub.subject_name,
			u.username
		FROM summaries s
		JOIN subjects sub ON s.subject_id = sub.id
		JOIN app_users u ON s.user_id = u.id
		WHERE (
			$1 = ''
			OR s.title ILIKE '%' || $1 || '%'
			OR s.content ILIKE '%' || $1 || '%'
		)
		AND ($2::integer IS NULL OR s.subject_id = $2)
		ORDER BY s.created_at DESC, s.id DESC
	`, q, subjectID)

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot get summaries",
		})
		return
	}
	defer rows.Close()

	summaries := []map[string]any{}

	for rows.Next() {
		var id, subject, user int
		var title, content, subjectName, username string
		var createdAt, updatedAt sql.NullTime

		err := rows.Scan(
			&id,
			&title,
			&content,
			&subject,
			&user,
			&createdAt,
			&updatedAt,
			&subjectName,
			&username,
		)

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Cannot read summary data",
			})
			return
		}

		summaries = append(summaries, map[string]any{
			"id":           id,
			"title":        title,
			"content":      content,
			"subject_id":   subject,
			"user_id":      user,
			"subject_name": subjectName,
			"username":     username,
			"created_at":   createdAt,
			"updated_at":   updatedAt,
		})
	}

	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot read summaries",
		})
		return
	}

	writeJSON(w, http.StatusOK, summaries)
}

// API ดูรายละเอียดสรุปตาม ID
func summaryDetailHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid summary ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var title, content, subjectName, username string
	var summaryID, subjectID, userID int
	var createdAt, updatedAt sql.NullTime

	err = db.QueryRowContext(ctx, `
		SELECT
			s.id,
			s.title,
			s.content,
			s.subject_id,
			s.user_id,
			s.created_at,
			s.updated_at,
			sub.subject_name,
			u.username
		FROM summaries s
		JOIN subjects sub ON s.subject_id = sub.id
		JOIN app_users u ON s.user_id = u.id
		WHERE s.id = $1
	`, id).Scan(
		&summaryID,
		&title,
		&content,
		&subjectID,
		&userID,
		&createdAt,
		&updatedAt,
		&subjectName,
		&username,
	)

	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Summary not found",
		})
		return
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot get summary",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":           summaryID,
		"title":        title,
		"content":      content,
		"subject_id":   subjectID,
		"user_id":      userID,
		"subject_name": subjectName,
		"username":     username,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
	})
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type Claims struct {
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req LoginRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON data",
		})
		return
	}

	req.Email = strings.TrimSpace(req.Email)

	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Email and password are required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var (
		id           int
		username     string
		email        string
		passwordHash string
		role         string
	)

	err := db.QueryRowContext(ctx, `
        SELECT id, username, email, password_hash, role
        FROM app_users
        WHERE email = $1
    `, req.Email).Scan(
		&id,
		&username,
		&email,
		&passwordHash,
		&role,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Invalid email or password",
			})
			return
		}

		log.Println("Login database error:", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot login",
		})
		return
	}

	err = bcrypt.CompareHashAndPassword(
		[]byte(passwordHash),
		[]byte(req.Password),
	)

	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Invalid email or password",
		})
		return
	}

	secret := os.Getenv("JWT_SECRET")

	if secret == "" {
		log.Println("JWT_SECRET is missing")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Server configuration error",
		})
		return
	}

	claims := Claims{
		UserID:   id,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.Itoa(id),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString([]byte(secret))

	if err != nil {
		log.Println("JWT error:", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Cannot create token",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "Login successful",
		"token":   tokenString,
		"user": map[string]any{
			"id":       id,
			"username": username,
			"email":    email,
			"role":     role,
		},
	})
}

type contextKey string

const userClaimsKey contextKey = "userClaims"

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// อ่าน Authorization Header
		authHeader := r.Header.Get("Authorization")

		parts := strings.Fields(authHeader)

		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Missing or invalid authorization header",
			})
			return
		}

		tokenString := parts[1]

		// อ่าน JWT Secret
		secret := os.Getenv("JWT_SECRET")

		if secret == "" {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Server configuration error",
			})
			return
		}

		// ตรวจสอบ Token
		claims := &Claims{}

		token, err := jwt.ParseWithClaims(
			tokenString,
			claims,
			func(token *jwt.Token) (any, error) {
				if token.Method != jwt.SigningMethodHS256 {
					return nil, errors.New("unexpected signing method")
				}

				return []byte(secret), nil
			},
			jwt.WithValidMethods([]string{"HS256"}),
		)

		if err != nil || !token.Valid {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Invalid or expired token",
			})
			return
		}

		// เก็บข้อมูลผู้ใช้ไว้ใน Request Context
		ctx := context.WithValue(r.Context(), userClaimsKey, claims)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func meHandler(w http.ResponseWriter, r *http.Request) {

	claims, ok := r.Context().Value(userClaimsKey).(*Claims)

	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Unauthorized",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":  "Authentication successful",
		"user_id":  claims.UserID,
		"username": claims.Username,
		"role":     claims.Role,
	})
}

type CreateSummaryRequest struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	SubjectID int    `json:"subject_id"`
}

func createSummaryHandler(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(userClaimsKey).(*Claims)

	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Unauthorized",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req CreateSummaryRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON data",
		})
		return
	}

	req.Title = strings.TrimSpace(req.Title)

	if req.Title == "" || strings.TrimSpace(req.Content) == "" || req.SubjectID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Title, content and subject_id are required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var (
		id        int
		createdAt time.Time
		updatedAt time.Time
	)

	err := db.QueryRowContext(ctx, `
        INSERT INTO summaries
            (title, content, subject_id, user_id, created_at, updated_at)
        VALUES ($1, $2, $3, $4, NOW(), NOW())
        RETURNING id, created_at, updated_at
    `,
		req.Title,
		req.Content,
		req.SubjectID,
		claims.UserID,
	).Scan(&id, &createdAt, &updatedAt)

	if err != nil {
		log.Println("Create summary error:", err)

		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Cannot create summary. Check subject_id.",
		})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"message": "Summary created successfully",
		"summary": map[string]any{
			"id":         id,
			"title":      req.Title,
			"content":    req.Content,
			"subject_id": req.SubjectID,
			"user_id":    claims.UserID,
			"created_at": createdAt,
			"updated_at": updatedAt,
		},
	})
}

func main() {
	http.Handle(
		"POST /api/summaries",
		authMiddleware(http.HandlerFunc(createSummaryHandler)),
	)
	// โหลดค่าจากไฟล์ .env
	http.Handle(
		"GET /api/me",
		authMiddleware(http.HandlerFunc(meHandler)),
	)
	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	var err error
	db, err = sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatal("Cannot open database:", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		log.Fatal("Cannot connect to Supabase:", err)
	}

	log.Println("Connected to Supabase successfully!")

	// กำหนด API
	http.HandleFunc("GET /api/health", healthHandler)
	http.HandleFunc("GET /api/db-test", dbTestHandler)
	http.HandleFunc("GET /api/subjects", subjectsHandler)
	http.HandleFunc("GET /api/summaries", summariesHandler)
	http.HandleFunc("GET /api/summaries/{id}", summaryDetailHandler)
	http.HandleFunc("POST /api/register", registerHandler)
	http.HandleFunc("POST /api/login", loginHandler)

	log.Println("StudyShare Backend running on port 8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
