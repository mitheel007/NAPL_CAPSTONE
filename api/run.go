package handler

import (
	"net/http"

	"gotest/internal/web"
)

func Run(w http.ResponseWriter, r *http.Request) { web.Run(w, r) }
