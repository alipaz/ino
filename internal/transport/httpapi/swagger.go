package httpapi

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerfiles "github.com/swaggo/files/v2"
)

//go:embed openapi.json
var openAPIDocument []byte

//go:embed swagger.html
var swaggerPage []byte

func registerSwaggerRoutes(router *gin.Engine) {
	page := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", swaggerPage)
	}
	router.GET("/swagger/", page)
	router.GET("/swagger/index.html", page)
	router.GET("/swagger/openapi.json", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json", openAPIDocument)
	})

	assets := http.FS(swaggerfiles.FS)
	router.StaticFileFS("/swagger/swagger-ui.css", "swagger-ui.css", assets)
	router.StaticFileFS("/swagger/swagger-ui-bundle.js", "swagger-ui-bundle.js", assets)
}
