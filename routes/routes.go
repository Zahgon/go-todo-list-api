package routes

import (
	handlers "github.com/antonkazachenko/go-todo-list-api/internal/server"
	"github.com/antonkazachenko/go-todo-list-api/internal/service"
	"github.com/antonkazachenko/go-todo-list-api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(taskService *service.TaskService) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	// Match the previous router's path handling: no trailing-slash or fixed-path
	// redirects, and route on the raw (still escaped) path when one is present.
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.UseRawPath = true
	// Let the engine resolve which methods a path accepts; the fallback handler
	// registered in main turns that into the response the API has always sent.
	r.HandleMethodNotAllowed = true

	h := handlers.NewHandlers(taskService)

	r.GET("/api/nextdate", gin.WrapF(h.HandleNextDate))
	r.POST("/api/task", gin.WrapF(middleware.Auth(h.HandleAddTask)))
	r.GET("/api/tasks", gin.WrapF(middleware.Auth(h.HandleGetTasks)))
	r.GET("/api/task", gin.WrapF(middleware.Auth(h.HandleGetTask)))
	r.PUT("/api/task", gin.WrapF(middleware.Auth(h.HandlePutTask)))
	r.DELETE("/api/task", gin.WrapF(middleware.Auth(h.HandleDeleteTask)))
	r.POST("/api/task/done", gin.WrapF(middleware.Auth(h.HandleDoneTask)))
	r.POST("/api/signin", gin.WrapF(h.HandleSignIn))

	return r
}
