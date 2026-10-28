package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"io"
	"net/http"
	"net/url"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"novelstudio/internal/worker"
	"sync"
	"time"
)

func respondError(c *echo.Context, err error) error {
	status := http.StatusInternalServerError
	message := "操作未完成，请稍后重试；原有内容已保留"
	switch {
	case errors.Is(err, authority.ErrUnavailable):
		status = 503
		message = "受保护撤销记录不可用，已停止此操作；请检查本地运行环境"
	case errors.Is(err, domain.ErrUnauthorized):
		status = 401
		message = err.Error()
	case errors.Is(err, domain.ErrForbidden):
		status = 403
		message = err.Error()
	case errors.Is(err, domain.ErrValidation):
		status = 400
		message = err.Error()
	case errors.Is(err, domain.ErrMaterialExpired):
		status = http.StatusGone
		message = err.Error()
	case errors.Is(err, service.ErrRecoveryIncomplete):
		status = 409
		message = err.Error()
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrContextBudget):
		status = 409
		message = err.Error()
	case errors.Is(err, service.ErrBudgetClosed), errors.Is(err, service.ErrBudgetExceeded):
		status = 409
		message = err.Error()
	case errors.Is(err, domain.ErrNotReady), errors.Is(err, domain.ErrNewerSchema):
		status = 503
		message = err.Error()
	}
	return c.JSON(status, map[string]string{"error": message})
}
func decode(c *echo.Context, target any) error {
	reader := http.MaxBytesReader(c.Response(), c.Request().Body, 1024*1024)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.ErrValidation
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.ErrValidation
	}
	return nil
}

// New owns HTTP concerns only; authorization and transactions live in service.
func New(db *sql.DB, origin string, runtimes ...*worker.Runtime) *echo.Echo {
	return newServer(db, origin, nil, runtimes...)
}

// Provider injection keeps public-source adapters outside HTTP policy. Normal
// startup uses the bounded Wikipedia provider; alternate adapters still pass
// through the same authentication, persistence and selection checks.
func NewWithResearchProvider(db *sql.DB, origin string, provider service.ResearchProvider, runtimes ...*worker.Runtime) *echo.Echo {
	return newServer(db, origin, provider, runtimes...)
}

func newServer(db *sql.DB, origin string, provider service.ResearchProvider, runtimes ...*worker.Runtime) *echo.Echo {
	e := echo.New()
	e.Use(middleware.Recover())
	svc := &service.Workspace{DB: db}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	startErr := storage.RunMigrations(ctx, db)
	cancel()
	configured, parseErr := url.Parse(origin)
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			r := c.Request()
			if parseErr != nil || r.Host != configured.Host {
				return respondError(c, domain.ErrForbidden)
			}
			if incoming := r.Header.Get("Origin"); incoming != "" && incoming != origin {
				return respondError(c, domain.ErrForbidden)
			}
			c.Response().Header().Set("X-Content-Type-Options", "nosniff")
			c.Response().Header().Set("Cache-Control", "no-store")
			c.Response().Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'; base-uri 'self'")
			if startErr != nil {
				return respondError(c, domain.ErrNotReady)
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if r.Header.Get("Origin") != origin {
					return respondError(c, domain.ErrForbidden)
				}
				if err := storage.CheckVersion(r.Context(), db); err != nil {
					return respondError(c, err)
				}
			}
			return next(c)
		}
	})
	e.GET("/api/health", func(c *echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			return respondError(c, domain.ErrNotReady)
		}
		return c.JSON(200, map[string]any{"database": "ready", "schema": storage.SchemaVersion})
	})
	e.GET("/api/setup", func(c *echo.Context) error {
		var n int
		if err := db.QueryRowContext(c.Request().Context(), "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"required": n == 0})
	})
	e.POST("/api/setup", func(c *echo.Context) error {
		var input struct{ Username, Password, Workspace string }
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if err := svc.Setup(c.Request().Context(), input.Username, input.Password, input.Workspace); err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, map[string]bool{"created": true})
	})
	var loginMu sync.Mutex
	var attempts int
	window := time.Now()
	e.POST("/api/login", func(c *echo.Context) error {
		loginMu.Lock()
		if time.Since(window) > time.Minute {
			window = time.Now()
			attempts = 0
		}
		attempts++
		limited := attempts > 20
		loginMu.Unlock()
		if limited {
			return c.JSON(429, map[string]string{"error": "登录尝试过于频繁，请一分钟后再试"})
		}
		var input struct{ Username, Password string }
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		token, err := svc.Login(c.Request().Context(), input.Username, input.Password)
		if err != nil {
			return respondError(c, err)
		}
		c.SetCookie(&http.Cookie{Name: "studio_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: configured.Scheme == "https", MaxAge: 43200})
		return c.JSON(200, map[string]bool{"authenticated": true})
	})
	secured := e.Group("/api", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			cookie, err := c.Cookie("studio_session")
			if err != nil {
				return respondError(c, domain.ErrUnauthorized)
			}
			actor, err := svc.Actor(c.Request().Context(), cookie.Value)
			if err != nil {
				return respondError(c, err)
			}
			c.Set("actor", actor)
			return next(c)
		}
	})
	actor := func(c *echo.Context) domain.Actor { return c.Get("actor").(domain.Actor) }
	registerNovelEntry(secured, svc, actor, provider)
	registerIP(secured, db, actor)
	registerReleases(secured, db, actor)
	registerModels(secured, db, actor)
	registerKnowledge(secured, db, actor)
	registerMemory(secured, db, actor)
	registerSkills(secured, db, actor)
	registerSkillDiscovery(secured, db, actor)
	registerCapabilities(secured, db, actor)
	registerDependencies(secured, db, actor)
	registerImports(secured, db, actor, provider)
	registerCharacterImages(secured, db, actor)
	var runtime *worker.Runtime
	if len(runtimes) > 0 {
		runtime = runtimes[0]
	}
	registerTasks(secured, db, actor, runtime)
	registerGeneration(secured, db, actor, runtime)
	registerIndexJobs(secured, db, actor, runtime)
	registerToolTrials(secured, db, actor, runtime)
	registerQualification(secured, db, actor, runtime)
	registerSemanticReviews(secured, db, actor, runtime)
	registerRecipes(secured, db, actor, runtime)
	registerProjectDeletion(secured, svc, actor)
	registerRetentionInventory(secured, svc, actor)
	secured.GET("/me", func(c *echo.Context) error { return c.JSON(200, actor(c)) })
	secured.POST("/logout", func(c *echo.Context) error {
		cookie, _ := c.Cookie("studio_session")
		if err := svc.Logout(c.Request().Context(), cookie.Value); err != nil {
			return respondError(c, err)
		}
		c.SetCookie(&http.Cookie{Name: "studio_session", Path: "/", Value: "", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		return c.JSON(200, map[string]bool{"logged_out": true})
	})
	secured.GET("/projects", func(c *echo.Context) error {
		items, err := svc.Projects(c.Request().Context(), actor(c))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	secured.POST("/projects", func(c *echo.Context) error {
		var input struct {
			Name        string `json:"name"`
			OperationID string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		p, err := svc.CreateProject(c.Request().Context(), actor(c).ID, input.Name, input.OperationID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, p)
	})
	secured.GET("/projects/:id", func(c *echo.Context) error {
		p, err := svc.Project(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, p)
	})
	secured.PATCH("/projects/:id", func(c *echo.Context) error {
		var input struct {
			Name     string `json:"name"`
			Revision int64  `json:"revision"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		p, err := svc.UpdateProject(c.Request().Context(), actor(c).ID, c.Param("id"), input.Name, input.Revision)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, p)
	})
	secured.GET("/members", func(c *echo.Context) error {
		items, err := svc.Members(c.Request().Context(), actor(c))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	secured.POST("/members", func(c *echo.Context) error {
		if actor(c).Role != "owner" {
			return respondError(c, domain.ErrForbidden)
		}
		var input struct{ Username, Password, Role string }
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		m, err := svc.CreateMember(c.Request().Context(), actor(c).ID, input.Username, input.Password, input.Role)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, m)
	})
	secured.PUT("/projects/:id/members/:user", func(c *echo.Context) error {
		var input struct{ Role string }
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if err := svc.SetMember(c.Request().Context(), actor(c).ID, c.Param("id"), c.Param("user"), input.Role, false); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"saved": true})
	})
	secured.DELETE("/projects/:id/members/:user", func(c *echo.Context) error {
		if err := svc.SetMember(c.Request().Context(), actor(c).ID, c.Param("id"), c.Param("user"), "", true); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"removed": true})
	})
	return e
}
