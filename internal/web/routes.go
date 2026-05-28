package web

import "net/http"

func (a *App) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.dashboard)

	mux.HandleFunc("GET /environments", a.listEnvironments)
	mux.HandleFunc("GET /environments/new", a.newEnvironmentForm)
	mux.HandleFunc("POST /environments", a.createEnvironment)
	mux.HandleFunc("GET /environments/{name}/edit", a.editEnvironmentForm)
	mux.HandleFunc("POST /environments/{name}/edit", a.updateEnvironment)
	mux.HandleFunc("POST /environments/{name}/delete", a.deleteEnvironment)

	mux.HandleFunc("GET /jobs", a.listJobs)
	mux.HandleFunc("GET /jobs/{id}", a.jobDetail)
	mux.HandleFunc("GET /jobs/{id}/runs/{env}/download", a.downloadRun)

	mux.HandleFunc("POST /run", a.runJob)
}
