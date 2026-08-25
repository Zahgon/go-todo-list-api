package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/antonkazachenko/go-todo-list-api/config"
	"github.com/antonkazachenko/go-todo-list-api/internal/service"
	storage "github.com/antonkazachenko/go-todo-list-api/internal/storage/sqlite"
	"github.com/antonkazachenko/go-todo-list-api/routes"
	"github.com/gin-gonic/gin"
)

func main() {
	db := storage.InitDB()
	defer db.Close()

	taskRepo := storage.NewSQLiteTaskRepository(db)

	taskService := service.NewTaskService(taskRepo)

	router := routes.RegisterRoutes(taskService)

	fileServer := http.FileServer(http.Dir("./web"))
	// The web assets used to be mounted as a GET wildcard covering every path
	// the API itself did not claim. A wildcard like that cannot share the
	// engine's tree with the /api routes, so it lives here instead: GET on any
	// unclaimed path still falls through to the file server, and every other
	// method answers the way it did when the wildcard was the only GET handler
	// left on that path.
	fallback := func(c *gin.Context) {
		header := c.Writer.Header()
		// The engine sets Allow before dispatching here; re-emit it below as one
		// header per method, and drop it entirely when a GET is about to succeed.
		allowed := header.Values("Allow")
		header.Del("Allow")

		if c.Request.Method == http.MethodGet {
			if filepath.Ext(c.Request.URL.Path) == ".css" {
				header.Set("Content-Type", "text/css")
			}
			// The engine buffers a 404 (or 405) status before it dispatches here.
			// The GET wildcard this replaces was an ordinary matched route, so it
			// started from the net/http default of 200. http.FileServer sets the
			// status itself for files (200), redirects (301), conditional hits
			// (304) and misses (404), but net/http's directory listing writes its
			// body with fmt.Fprintf and never calls WriteHeader -- the buffered
			// status would then be flushed instead. Reset it to 200 first so a
			// listing is a 200 again; every path that does set a status overrides
			// this line as it always did.
			c.Writer.WriteHeader(http.StatusOK)
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		for _, value := range allowed {
			for _, method := range strings.Split(value, ",") {
				if method = strings.TrimSpace(method); method != "" {
					header.Add("Allow", method)
				}
			}
		}
		header.Add("Allow", http.MethodGet)
		c.Writer.WriteHeader(http.StatusMethodNotAllowed)
		if _, err := c.Writer.Write(nil); err != nil {
			log.Printf("Failed to write response: %v", err)
		}
	}
	router.NoRoute(fallback)
	router.NoMethod(fallback)

	address := fmt.Sprintf(":%s", config.TODO_PORT)
	log.Printf("Starting server on %s", address)
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
