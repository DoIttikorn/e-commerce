package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/user"
)

// maxBodyBytes caps request bodies. Without it a single client can make the
// server allocate as much memory as it cares to send.
const maxBodyBytes = 1 << 20 // 1 MiB

// errMalformedJSON keeps a broken body a 400 rather than a 500.
var errMalformedJSON = errors.New("malformed json body")

// userResponse is what a user looks like on the wire.
//
// It has no password field at all, which is a stronger guarantee than
// remembering to blank one: the hash cannot be serialised because there is
// nowhere for it to go.
type userResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func toUserResponse(u user.User) userResponse {
	return userResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
	}
}

type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// updateRequest uses pointers so an omitted field is distinguishable from one
// set to the empty string. That distinction is the whole reason this endpoint
// is PATCH rather than PUT.
type updateRequest struct {
	Name  *string `json:"name"`
	Email *string `json:"email"`
}

type listResponse struct {
	Users  []userResponse `json:"users"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

type errorBody struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return errMalformedJSON
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The header and status are already sent, so a failure here cannot be
	// turned into an error response; it means the client went away.
	_ = json.NewEncoder(w).Encode(body)
}

// pagingFrom reads limit and offset, ignoring values that are not numbers. The
// service clamps whatever arrives, so a nonsense value becomes the default
// rather than a 400 — a query string is not worth failing a read over.
func pagingFrom(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	return limit, offset
}
