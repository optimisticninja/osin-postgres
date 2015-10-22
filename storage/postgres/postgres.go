package postgres

import (
	"database/sql"
	"github.com/RangelReale/osin"
	"log"
)

var schemas = []string{`CREATE TABLE client (
	id           text NOT NULL,
	secret 		 text NOT NULL,
	redirect_uri text NOT NULL,

    CONSTRAINT client_pk PRIMARY KEY (id)
)`, `CREATE TABLE IF NOT EXISTS authorize (
	client       text NOT NULL,
	code         text NOT NULL,
	expires_in   int NOT NULL,
	scope        text NOT NULL,
	redirect_uri text NOT NULL,
	state        text NOT NULL,
	created_at   timestamp with time zone NOT NULL,

    CONSTRAINT authorize_pk PRIMARY KEY (code)
)`, `CREATE TABLE IF NOT EXISTS access (
	client        text NOT NULL,
	authorize     text NOT NULL,
	previous      text NOT NULL,
	access_token  text NOT NULL,
	refresh_token text NOT NULL,
	expires_in    int NOT NULL,
	scope         text NOT NULL,
	redirect_uri  text NOT NULL,
	created_at    timestamp with time zone NOT NULL,

    CONSTRAINT access_pk PRIMARY KEY (access_token)
)`, `CREATE TABLE IF NOT EXISTS refresh (
	token         text NOT NULL,
	access        text NOT NULL,

    CONSTRAINT refresh_pk PRIMARY KEY (token)
)`}

type Storage struct {
	db *sql.DB
}

func New(db *sql.DB) *Storage {
	return &Storage{db}
}

func (s *Storage) CreateSchemas() error {
	for k, schema := range schemas {
		if _, err := s.db.Exec(schema); err != nil {
			log.Printf("Error creating schema %d: %s", k, schema)
			return err
		}
	}
	return nil
}

func (s *Storage) Clone() osin.Storage {
	return s
}

func (s *Storage) Close() {
}

func (s *Storage) GetClient(id string) (osin.Client, error) {
	row := s.db.QueryRow("SELECT id, secret, redirect_uri FROM client WHERE id=$1 LIMIT 1", id)
	var c osin.DefaultClient
	if err := row.Scan(&c.Id, &c.Secret, &c.RedirectUri); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Storage) UpdateClient(id, secret, redirectURI string) (osin.Client, error) {
	if _, err := s.db.Exec("UPDATE client SET (secret, redirect_uri) = ($2, $3) WHERE id=$1", id, secret, redirectURI); err != nil {
		return nil, err
	}
	return &osin.DefaultClient{id, secret, redirectURI, nil}, nil
}

func (s *Storage) CreateClient(id, secret, redirectURI string) (osin.Client, error) {
	_, err := s.db.Exec("INSERT INTO client (id, secret, redirect_uri) VALUES ($1, $2, $3)", id, secret, redirectURI)
	if err != nil {
		return nil, err
	}
	return &osin.DefaultClient{id, secret, redirectURI, nil}, nil
}

func (s *Storage) SaveAuthorize(data *osin.AuthorizeData) (err error) {
	_, err = s.db.Exec("INSERT INTO authorize (client, code, expires_in, scope, redirect_uri, state, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)", data.Client.GetId(), data.Code, data.ExpiresIn, data.Scope, data.RedirectUri, data.State, data.CreatedAt)
	return err
}

func (s *Storage) LoadAuthorize(code string) (*osin.AuthorizeData, error) {
	var data osin.AuthorizeData
	var cid string
	row := s.db.QueryRow("SELECT client, code, expires_in, scope, redirect_uri, state, created_at FROM authorize WHERE code=$1 LIMIT 1", code)
	if err := row.Scan(&cid, &data.Code, &data.ExpiresIn, &data.Scope, &data.RedirectUri, &data.State, &data.CreatedAt); err != nil {
		return nil, err
	}

	c, err := s.GetClient(cid)
	if err != nil {
		return nil, err
	}

	data.Client = c
	return &data, nil
}

func (s *Storage) RemoveAuthorize(code string) (err error) {
	_, err = s.db.Exec("DELETE FROM authorize WHERE code=$1", code)
	return err
}

func (s *Storage) SaveAccess(data *osin.AccessData) (err error) {
	prev := ""
	if data.AccessData != nil {
		prev = data.AccessData.AccessToken
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	if data.RefreshToken != "" {
		if err := saveRefresh(tx, data.RefreshToken, data.AccessToken); err != nil {
			return err
		}
	}

	_, err = tx.Exec("INSERT INTO access (client, authorize, previous, access_token, refresh_token, expires_in, scope, redirect_uri, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)", data.Client.GetId(), data.AuthorizeData.Code, prev, data.AccessToken, data.RefreshToken, data.ExpiresIn, data.Scope, data.RedirectUri, data.CreatedAt)
	if err != nil {
		if rbe := tx.Rollback(); rbe != nil {
			return rbe
		}
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	return nil
}

func (s *Storage) LoadAccess(code string) (*osin.AccessData, error) {
	var cid, prevAccessToken, authorizeCode string
	var result osin.AccessData
	row := s.db.QueryRow("SELECT client, authorize, previous, access_token, refresh_token, expires_in, scope, redirect_uri, created_at FROM access WHERE access_token=$1 LIMIT 1", code)
	err := row.Scan(&cid, &authorizeCode, &prevAccessToken, &result.AccessToken, &result.RefreshToken, &result.ExpiresIn, &result.Scope, &result.RedirectUri, &result.CreatedAt)

	client, err := s.GetClient(cid)
	if err != nil {
		return nil, err
	}
	result.Client = client

	authorize, err := s.LoadAuthorize(authorizeCode)
	if err != nil {
		return nil, err
	}
	result.AuthorizeData = authorize

	if prevAccessToken != "" {
		prevAccess, err := s.LoadAccess(prevAccessToken)
		if err != nil {
			return nil, err
		}
		result.AccessData = prevAccess
	}

	return &result, err
}

func (s *Storage) RemoveAccess(code string) (err error) {
	st, err := s.db.Prepare("DELETE FROM access WHERE access_token=$1")
	if err != nil {
		return
	}
	_, err = st.Exec(code)
	return err
}

func saveRefresh(tx *sql.Tx, refresh, access string) (err error) {
	_, err = tx.Exec("INSERT INTO refresh (token, access) VALUES ($1, $2)", refresh, access)
	if err != nil {
		if rbe := tx.Rollback(); rbe != nil {
			return rbe
		}
	}
	return err
}

func (s *Storage) LoadRefresh(code string) (*osin.AccessData, error) {
	row := s.db.QueryRow("SELECT access FROM refresh WHERE token=$1 LIMIT 1", code)
	var access string
	if err := row.Scan(&access); err != nil {
		return nil, err
	}
	return s.LoadAccess(access)
}

func (s *Storage) RemoveRefresh(code string) error {
	st, err := s.db.Prepare("DELETE FROM refresh WHERE token=$1")
	if err != nil {
		return err
	}
	_, err = st.Exec(code)
	return err
}