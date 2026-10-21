package main

// Accounts: one JSON file, written atomically. Passwords are never stored: only a PBKDF2-SHA256
// hash with a random salt per account. Login sessions are random tokens kept in a second file so a
// server restart does not log everybody out.

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pbkdfIterations = 210000
	sessionLifetime = 60 * 24 * time.Hour
)

var userNameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]{3,16}$`)

type account struct {
	Name    string    `json:"name"`
	Salt    string    `json:"salt"`
	Hash    string    `json:"hash"`
	Iter    int       `json:"iter"`
	Created time.Time `json:"created"`
	Friends []string  `json:"friends,omitempty"`
	Blocked []string  `json:"blocked,omitempty"` // [block] players this one does not want to hear from
}

type session struct {
	User    string    `json:"user"`
	Expires time.Time `json:"expires"`
}

type store struct {
	mu       sync.Mutex
	dir      string
	users    map[string]*account // key: lower-case name
	sessions map[string]*session // key: sha256 of the token (the token itself is never stored)
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{dir: dir, users: map[string]*account{}, sessions: map[string]*session{}}
	if err := readJSON(filepath.Join(dir, "users.json"), &s.users); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "sessions.json"), &s.sessions); err != nil {
		return nil, err
	}
	now := time.Now()
	for t, se := range s.sessions {
		if now.After(se.Expires) {
			delete(s.sessions, t)
		}
	}
	return s, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *store) saveUsersLocked() error {
	return writeJSON(filepath.Join(s.dir, "users.json"), s.users)
}
func (s *store) saveSessionsLocked() error {
	return writeJSON(filepath.Join(s.dir, "sessions.json"), s.sessions)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// tokenKey: sessions are kept by the token's hash, so a leaked sessions.json cannot be replayed.
func tokenKey(tok string) string {
	sum := sha256.Sum256([]byte("recomp-online-session\x00" + tok))
	return hex.EncodeToString(sum[:])
}

func hashPassword(pass string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, iter, 32)
}

var (
	errBadName     = errors.New("el usuario debe tener de 3 a 16 letras, números, _ o -")
	errBadPassword = errors.New("la contraseña debe tener al menos 6 caracteres")
	errNameTaken   = errors.New("ese usuario ya existe")
	errLogin       = errors.New("usuario o contraseña incorrectos")
)

func (s *store) register(name, pass string) error {
	if !userNameRe.MatchString(name) {
		return errBadName
	}
	if len(pass) < 6 || len(pass) > 128 {
		return errBadPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	h, err := hashPassword(pass, salt, pbkdfIterations)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(name)
	if _, ok := s.users[key]; ok {
		return errNameTaken
	}
	s.users[key] = &account{Name: name, Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(h), Iter: pbkdfIterations, Created: time.Now().UTC()}
	return s.saveUsersLocked()
}

// login checks the password and returns a new session token and the account's display name.
func (s *store) login(name, pass string) (string, string, error) {
	s.mu.Lock()
	a, ok := s.users[strings.ToLower(name)]
	s.mu.Unlock()
	if !ok {
		// same work as a real check, so a missing user is not faster to answer
		_, _ = hashPassword(pass, []byte("recomp-online-dummy"), pbkdfIterations)
		return "", "", errLogin
	}
	salt, _ := hex.DecodeString(a.Salt)
	want, _ := hex.DecodeString(a.Hash)
	got, err := hashPassword(pass, salt, a.Iter)
	if err != nil || subtle.ConstantTimeCompare(got, want) != 1 {
		return "", "", errLogin
	}
	tok := randomHex(24)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[tokenKey(tok)] = &session{User: a.Name, Expires: time.Now().Add(sessionLifetime)}
	_ = s.saveSessionsLocked()
	return tok, a.Name, nil
}

func (s *store) userForToken(tok string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(tok) != 48 {
		return "", false
	}
	se, ok := s.sessions[tokenKey(tok)]
	if !ok || time.Now().After(se.Expires) {
		return "", false
	}
	return se.User, true
}

func (s *store) logout(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenKey(tok))
	_ = s.saveSessionsLocked()
}

func (s *store) changePassword(user, oldPass, newPass string) error {
	if _, _, err := s.login(user, oldPass); err != nil {
		return err
	}
	if len(newPass) < 6 || len(newPass) > 128 {
		return errBadPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	h, err := hashPassword(newPass, salt, pbkdfIterations)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.users[strings.ToLower(user)]
	a.Salt, a.Hash, a.Iter = hex.EncodeToString(salt), hex.EncodeToString(h), pbkdfIterations
	// every other session of this account ends
	for t, se := range s.sessions {
		if strings.EqualFold(se.User, user) {
			delete(s.sessions, t)
		}
	}
	_ = s.saveSessionsLocked()
	return s.saveUsersLocked()
}

func (s *store) exists(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.users[strings.ToLower(name)]
	if !ok {
		return "", false
	}
	return a.Name, true
}

// search: account names containing q (any case), those that start with it first, then by name.
func (s *store) search(q string, max int) []string {
	q = strings.ToLower(q)
	s.mu.Lock()
	var pre, mid []string
	for k, a := range s.users {
		switch i := strings.Index(k, q); {
		case i == 0:
			pre = append(pre, a.Name)
		case i > 0:
			mid = append(mid, a.Name)
		}
	}
	s.mu.Unlock()
	byName := func(l []string) {
		sort.Slice(l, func(i, j int) bool { return strings.ToLower(l[i]) < strings.ToLower(l[j]) })
	}
	byName(pre)
	byName(mid)
	out := append(pre, mid...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func (s *store) friends(user string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.users[strings.ToLower(user)]
	if a == nil {
		return nil
	}
	return append([]string(nil), a.Friends...)
}

// [block] A blocked player cannot invite the one who blocked them, nor be invited by them, nor share a room with
// them, and their room chat does not reach them; the blocker's launcher also hides them. Blocking someone also
// takes them off the blocker's friends.
func (s *store) blocked(user string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.users[strings.ToLower(user)]
	if a == nil {
		return nil
	}
	return append([]string(nil), a.Blocked...)
}

// isBlocked: user blocked other.
func (s *store) isBlocked(user, other string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.users[strings.ToLower(user)]
	if a == nil {
		return false
	}
	for _, n := range a.Blocked {
		if strings.EqualFold(n, other) {
			return true
		}
	}
	return false
}

// blockedEither: one of the two blocked the other.
func (s *store) blockedEither(a, b string) bool { return s.isBlocked(a, b) || s.isBlocked(b, a) }

func (s *store) setBlocked(user, other string, block bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.users[strings.ToLower(user)]
	o, ok := s.users[strings.ToLower(other)]
	if a == nil || !ok || strings.EqualFold(user, other) {
		return errors.New("ese usuario no existe")
	}
	out := a.Blocked[:0]
	for _, n := range a.Blocked {
		if !strings.EqualFold(n, o.Name) {
			out = append(out, n)
		}
	}
	if block {
		out = append(out, o.Name)
		friends := a.Friends[:0]
		for _, n := range a.Friends {
			if !strings.EqualFold(n, o.Name) {
				friends = append(friends, n)
			}
		}
		a.Friends = friends
	}
	a.Blocked = out
	return s.saveUsersLocked()
}

func (s *store) setFriend(user, friend string, add bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.users[strings.ToLower(user)]
	f, ok := s.users[strings.ToLower(friend)]
	if a == nil || !ok || strings.EqualFold(user, friend) {
		return errors.New("ese usuario no existe")
	}
	if add {
		for _, n := range a.Blocked {
			if strings.EqualFold(n, f.Name) {
				return errors.New("lo tienes bloqueado: desbloquéalo primero")
			}
		}
	}
	out := a.Friends[:0]
	for _, n := range a.Friends {
		if !strings.EqualFold(n, f.Name) {
			out = append(out, n)
		}
	}
	if add {
		out = append(out, f.Name)
	}
	a.Friends = out
	return s.saveUsersLocked()
}
