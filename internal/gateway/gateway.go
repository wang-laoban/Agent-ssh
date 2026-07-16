package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"test-agent/internal/auth"
	"test-agent/internal/config"
	"test-agent/internal/executor"
	"test-agent/internal/taskmanager"
)

// APIResponse is the unified JSON response envelope.
type APIResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// Error codes.
const (
	CodeOK              = 0
	CodeBadRequest      = 1001
	CodeUnauthorized    = 1002
	CodeForbidden       = 1003
	CodeNotFound        = 1004
	CodeRequestTimeout  = 1005
	CodeTooManyRequests = 1006
	CodeFileTooLarge    = 1011
	CodeInvalidFileName = 1012
	CodeInternalError   = 2001
	CodeUploadFailed    = 2002
	CodeFileAlreadyExists = 2003
)

// Server wraps the HTTP server and dependencies.
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
	auth       *auth.TokenManager
	exec       *executor.Executor
	tm         *taskmanager.TaskManager
	logger     *slog.Logger
}

// New creates a new HTTP gateway server.
func New(cfg config.ServerConfig, authMgr *auth.TokenManager, exec *executor.Executor, tm *taskmanager.TaskManager, logger *slog.Logger) *Server {
	s := &Server{
		mux:    http.NewServeMux(),
		auth:   authMgr,
		exec:   exec,
		tm:     tm,
		logger: logger,
	}

	s.registerRoutes()

	s.httpServer = &http.Server{
		Addr:         cfg.ListenAddr(),
		Handler:      s.recoverMiddleware(s.requestIDMiddleware(s.mux)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/v1/exec", s.authMiddleware(s.handleExec))
	s.mux.HandleFunc("POST /api/v1/tasks", s.authMiddleware(s.handleCreateTask))
	s.mux.HandleFunc("GET /api/v1/tasks/{task_id}", s.authMiddleware(s.handleGetTask))
	s.mux.HandleFunc("DELETE /api/v1/tasks/{task_id}", s.authMiddleware(s.handleDeleteTask))
	s.mux.HandleFunc("POST /api/v1/auth/rotate", s.authMiddleware(s.handleRotate))
	s.mux.HandleFunc("POST /api/v1/upload", s.authMiddleware(s.handleUpload))
}

// Start begins listening for HTTP requests.
func (s *Server) Start() error {
	s.logger.Info("http_server_starting", slog.String("addr", s.httpServer.Addr))
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Middleware: recover from panics.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("handler_panic",
					slog.String("request_id", requestIDFromContext(r.Context())),
					slog.Any("panic", rec),
				)
				respondJSON(w, http.StatusInternalServerError, CodeInternalError, "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Middleware: inject/request X-Request-ID.
func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = generateRequestID()
		}
		ctx := withRequestID(r.Context(), reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Middleware: bearer token authorization.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			s.logger.Warn("missing_authorization_header",
				slog.String("request_id", requestIDFromContext(r.Context())),
			)
			respondJSON(w, http.StatusUnauthorized, CodeUnauthorized, "missing authorization header", nil)
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			respondJSON(w, http.StatusUnauthorized, CodeUnauthorized, "invalid authorization format", nil)
			return
		}
		token := strings.TrimPrefix(authHeader, prefix)

		if !s.auth.VerifyToken(token) {
			s.logger.Warn("token_mismatch",
				slog.String("request_id", requestIDFromContext(r.Context())),
			)
			respondJSON(w, http.StatusUnauthorized, CodeUnauthorized, "token mismatch, retrieve latest from server file", nil)
			return
		}

		next(w, r)
	}
}

// Handlers.

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, CodeOK, "", map[string]string{"status": "alive"})
}

// execRequest mirrors the POST /exec request body.
type execRequest struct {
	Command string            `json:"command"`
	Timeout int               `json:"timeout"`
	WorkDir string            `json:"work_dir"`
	EnvVars map[string]string `json:"env_vars"`
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqID := requestIDFromContext(ctx)

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, CodeBadRequest, "invalid json", nil)
		return
	}

	if strings.TrimSpace(req.Command) == "" {
		respondJSON(w, http.StatusBadRequest, CodeBadRequest, "command is required", nil)
		return
	}

	if err := s.exec.Validate(req.Command); err != nil {
		code := CodeForbidden
		if errors.Is(err, executor.ErrNotAllowed) {
			code = CodeForbidden
		}
		s.logger.Warn("command_validation_failed",
			slog.String("request_id", reqID),
			slog.String("error", err.Error()),
		)
		respondJSON(w, http.StatusForbidden, code, err.Error(), nil)
		return
	}

	timeout := time.Duration(req.Timeout) * time.Second
	if req.Timeout <= 0 {
		timeout = 0 // let executor apply default
	}

	result := s.exec.ExecuteWithID(ctx, reqID, req.Command, timeout)

	// Note: EnvVars and WorkDir from the request are ignored in this minimal
	// implementation to keep the first version focused; they can be extended later.

	if result.Stderr != "" && strings.Contains(result.Stderr, "[timeout]") {
		respondJSON(w, http.StatusRequestTimeout, CodeRequestTimeout, "command execution timeout", result)
		return
	}

	respondJSON(w, http.StatusOK, CodeOK, "", result)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqID := requestIDFromContext(ctx)

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, CodeBadRequest, "invalid json", nil)
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		respondJSON(w, http.StatusBadRequest, CodeBadRequest, "command is required", nil)
		return
	}
	if err := s.exec.Validate(req.Command); err != nil {
		s.logger.Warn("task_command_validation_failed",
			slog.String("request_id", reqID),
			slog.String("error", err.Error()),
		)
		respondJSON(w, http.StatusForbidden, CodeForbidden, err.Error(), nil)
		return
	}

	timeout := time.Duration(req.Timeout) * time.Second
	if req.Timeout <= 0 {
		timeout = 0
	}

	taskID, err := s.tm.Submit(req.Command, timeout)
	if err != nil {
		if errors.Is(err, taskmanager.ErrMaxRunning) {
			respondJSON(w, http.StatusTooManyRequests, CodeTooManyRequests, err.Error(), nil)
			return
		}
		respondJSON(w, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusAccepted, CodeOK, "", map[string]string{"task_id": taskID})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	task, err := s.tm.Get(taskID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, CodeNotFound, err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, CodeOK, "", task)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	task, err := s.tm.Cancel(taskID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, CodeNotFound, err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, CodeOK, "", task)
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	s.auth.TriggerManualRotation()
	// Give the rotation goroutine a moment to finish.
	time.Sleep(100 * time.Millisecond)
	respondJSON(w, http.StatusOK, CodeOK, "token rotation triggered", nil)
}

// handleUpload receives a multipart file and saves it securely under the
// configured upload directory.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqID := requestIDFromContext(ctx)

	maxSize := s.exec.MaxUploadSize()
	if maxSize <= 0 {
		maxSize = 64 << 20
	}

	// Limit the whole request body to max upload size + 1 MiB of form overhead.
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+(1<<20))

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) || strings.Contains(err.Error(), "request body too large") {
			respondJSON(w, http.StatusRequestEntityTooLarge, CodeFileTooLarge,
				fmt.Sprintf("file too large, max %d bytes", maxSize), nil)
			return
		}
		respondJSON(w, http.StatusBadRequest, CodeBadRequest,
			"invalid multipart form: "+err.Error(), nil)
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		respondJSON(w, http.StatusBadRequest, CodeBadRequest, "missing file field", nil)
		return
	}
	defer file.Close()

	originalName := header.Filename
	safeName, err := sanitizeFilename(originalName)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, CodeInvalidFileName, err.Error(), nil)
		return
	}

	if exts := s.exec.AllowedExtensions(); len(exts) > 0 {
		ext := strings.ToLower(filepath.Ext(safeName))
		allowed := false
		for _, e := range exts {
			if strings.ToLower(strings.TrimSpace(e)) == ext {
				allowed = true
				break
			}
		}
		if !allowed {
			respondJSON(w, http.StatusForbidden, CodeForbidden,
				"file extension not allowed", nil)
			return
		}
	}

	savedName := fmt.Sprintf("upload_%s_%s", reqID[:8], safeName)
	uploadDir := s.exec.UploadDir()

	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		s.logger.Error("create_upload_dir_failed",
			slog.String("request_id", reqID),
			slog.String("error", err.Error()))
		respondJSON(w, http.StatusInternalServerError, CodeInternalError,
			"failed to create upload directory", nil)
		return
	}

	targetPath := filepath.Join(uploadDir, savedName)
	cleanTarget := filepath.Clean(targetPath)
	cleanUploadDir := filepath.Clean(uploadDir)
	if !strings.HasPrefix(cleanTarget, cleanUploadDir+string(os.PathSeparator)) {
		respondJSON(w, http.StatusBadRequest, CodeInvalidFileName, "invalid file path", nil)
		return
	}

	size, err := saveUploadedFile(file, targetPath)
	if err != nil {
		if os.IsExist(err) {
			respondJSON(w, http.StatusConflict, CodeFileAlreadyExists,
				"target file already exists", nil)
			return
		}
		s.logger.Error("save_upload_failed",
			slog.String("request_id", reqID),
			slog.String("error", err.Error()))
		respondJSON(w, http.StatusInternalServerError, CodeUploadFailed,
			"failed to save file", nil)
		return
	}

	s.logger.Info("file_uploaded",
		slog.String("request_id", reqID),
		slog.String("saved_path", targetPath),
		slog.Int64("size_bytes", size),
	)

	respondJSON(w, http.StatusOK, CodeOK, "", map[string]interface{}{
		"original_name": originalName,
		"saved_name":    savedName,
		"saved_path":    targetPath,
		"size_bytes":    size,
	})
}

var filenameSafeRE = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// sanitizeFilename returns a safe base name for the uploaded file.
func sanitizeFilename(name string) (string, error) {
	base := filepath.Base(name)
	if base == "" || base == "." || base == ".." {
		return "", errors.New("invalid file name")
	}
	safe := filenameSafeRE.ReplaceAllString(base, "_")
	if safe == "" || safe == "." || safe == ".." {
		return "", errors.New("invalid file name after sanitization")
	}
	return safe, nil
}

// saveUploadedFile atomically writes src to dst using a temp file + rename.
func saveUploadedFile(src io.Reader, dst string) (int64, error) {
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, src)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

// Helpers.

func respondJSON(w http.ResponseWriter, status int, code int, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{
		Code:    code,
		Message: message,
		Data:    data,
	})
}

// request id context helpers.

type ctxKey string

const requestIDKey ctxKey = "request_id"

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	if id == "" {
		id = generateRequestID()
	}
	return id
}

// generateRequestID returns a random hex string suitable for request tracing.
func generateRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
