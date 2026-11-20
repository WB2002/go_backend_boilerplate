package main

import (
	"strings"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "go_backend_boilerplate/docs/swagger"
	"go_backend_boilerplate/internal/config"
)

// registerSwagger registers Swagger UI and OpenAPI documentation endpoints on the router
// when running outside production or when explicitly enabled via configuration.
func registerSwagger(r gin.IRoutes, appCfg config.AppConfig) {
	if strings.EqualFold(strings.TrimSpace(appCfg.Env), "production") && !appCfg.EnableSwagger {
		return
	}
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
}
