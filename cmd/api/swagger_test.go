package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSwaggerEndpointMount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	registerSwagger(r)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/swagger/index.html", nil)
	req.RequestURI = "/swagger/index.html"
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Swagger UI")

	wDoc := httptest.NewRecorder()
	reqDoc, _ := http.NewRequest("GET", "/swagger/doc.json", nil)
	reqDoc.RequestURI = "/swagger/doc.json"
	r.ServeHTTP(wDoc, reqDoc)

	assert.Equal(t, http.StatusOK, wDoc.Code)
	assert.Contains(t, wDoc.Body.String(), "Go Backend Boilerplate API")
}
