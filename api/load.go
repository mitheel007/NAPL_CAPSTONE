package handler

import (
	"net/http"

	"gotest/internal/web"
)

func Load(w http.ResponseWriter, r *http.Request) { web.Load(w, r) }
