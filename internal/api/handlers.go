package api

import (
	"net/http"
)

func (s *Server) registerRoutes() {
	s.registerAccountRoutes()
	s.registerBrowseRoutes()
	s.registerTrashRoutes()
	s.registerActionRoutes()
	s.mux.HandleFunc("GET /events", s.streamEvents)
}

func (s *Server) registerAccountRoutes() {
	s.handle("GET /status", func(r *http.Request) (any, error) {
		return s.service.Status(r.Context())
	})
	s.handle("POST /signin", func(r *http.Request) (any, error) {
		authURL, err := s.service.BeginSignIn(r.Context())
		if err != nil {
			return nil, err
		}
		return authURLBody{AuthURL: authURL}, nil
	})
	s.handle("POST /signout", func(r *http.Request) (any, error) {
		return nil, s.service.SignOut(r.Context())
	})
	s.handlePassword("POST /create", func(r *http.Request, password []byte) error {
		return s.service.CreateVault(r.Context(), password, r.URL.Query().Get("deviceName"))
	})
	s.handlePassword("POST /join", func(r *http.Request, password []byte) error {
		mode, err := parseJoinMode(r.URL.Query().Get("mode"))
		if err != nil {
			return err
		}
		return s.service.JoinVault(r.Context(), password, r.URL.Query().Get("deviceName"), mode)
	})
	s.handlePassword("POST /unlock", func(r *http.Request, password []byte) error {
		return s.service.Unlock(r.Context(), password)
	})
	s.handle("POST /lock", func(r *http.Request) (any, error) {
		return nil, s.service.Lock(r.Context())
	})
}

func (s *Server) registerBrowseRoutes() {
	s.handle("GET /tree", func(r *http.Request) (any, error) {
		return s.service.Tree(r.Context(), r.URL.Query().Get("path"))
	})
	s.handle("GET /commits", func(r *http.Request) (any, error) {
		query := r.URL.Query()
		limit, err := parseLimitQuery(query)
		if err != nil {
			return nil, err
		}
		return s.service.Commits(r.Context(), query.Get("before"), limit, query.Get("path"))
	})
	s.handle("GET /devices", func(r *http.Request) (any, error) {
		return s.service.Devices(r.Context())
	})
	handleJSON(s, "POST /devices/retire", func(r *http.Request, body idBody) (any, error) {
		return nil, s.service.RetireDevice(r.Context(), body.ID)
	})
	s.handle("GET /settings", func(r *http.Request) (any, error) {
		return s.service.Settings(r.Context())
	})
	handleJSON(s, "PUT /settings", func(r *http.Request, settings Settings) (any, error) {
		return nil, s.service.UpdateSettings(r.Context(), settings)
	})
}

func (s *Server) registerTrashRoutes() {
	s.handle("GET /trash", func(r *http.Request) (any, error) {
		return s.service.Trash(r.Context())
	})
	handleJSON(s, "POST /trash/restore", func(r *http.Request, body idBody) (any, error) {
		restoredPath, err := s.service.RestoreTrash(r.Context(), body.ID)
		if err != nil {
			return nil, err
		}
		return pathBody{Path: restoredPath}, nil
	})
	handleJSON(s, "POST /trash/purge", func(r *http.Request, body idBody) (any, error) {
		return nil, s.service.PurgeTrash(r.Context(), body.ID)
	})
	s.handle("POST /trash/empty", func(r *http.Request) (any, error) {
		return s.service.EmptyTrash(r.Context())
	})
}

func (s *Server) registerActionRoutes() {
	handleJSON(s, "POST /download", func(r *http.Request, body pathBody) (any, error) {
		return nil, s.service.Download(r.Context(), body.Path)
	})
	handleJSON(s, "POST /free", func(r *http.Request, body pathBody) (any, error) {
		return nil, s.service.Free(r.Context(), body.Path)
	})
	s.handle("POST /sync", func(r *http.Request) (any, error) {
		return nil, s.service.SyncNow(r.Context())
	})
	s.handle("POST /verify", func(r *http.Request) (any, error) {
		deep, err := parseBoolQuery(r.URL.Query(), "deep")
		if err != nil {
			return nil, err
		}
		return s.service.Verify(r.Context(), deep)
	})
	s.handle("POST /restore/pause", func(r *http.Request) (any, error) {
		paused, err := parseBoolQuery(r.URL.Query(), "paused")
		if err != nil {
			return nil, err
		}
		return nil, s.service.PauseRestore(r.Context(), paused)
	})
}
