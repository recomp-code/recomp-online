package main

// Online statistics: fights won and lost, matches and time played online, per player and game. Only online matches
// count, and only what the server can trust:
//   - a fight counts once both players' games reported the same result for it (the two games of a match run the same
//     simulation, so honest launchers always agree; a fight one side never reports, or reports differently, counts
//     for nobody);
//   - the launchers report each fight as it ends, so quitting a match that is going badly does not undo the fights
//     already played;
//   - the time is the match's own, measured here from its start to its end.
// One JSON file (stats.json), rewritten when something changes.

import (
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type playerStats struct {
	User    string         `json:"user"`
	Wins    int            `json:"wins"`
	Losses  int            `json:"losses"`
	Matches int            `json:"matches"`
	Seconds int64          `json:"seconds"`
	Chars   map[string]int `json:"chars,omitempty"` // character id (the game's) -> fights played with it
	Last    time.Time      `json:"last"`
}

// fightReport: what one game saw at the end of a fight.
type fightReport struct {
	Winner int `json:"winner"` // 1 or 2 (the match's player 1 / 2); 0 = the game could not tell (not counted)
	C1     int `json:"c1"`     // the characters, as the game numbers them
	C2     int `json:"c2"`
}

// matchFights: the fights of one match while it runs and a while after (a report can arrive after the end).
type matchFights struct {
	game, p1, p2 string
	reports      map[int][2]*fightReport // fight number -> each player's report
	counted      map[int]bool
}

type statsStore struct {
	mu      sync.Mutex
	path    string
	players map[string]map[string]*playerStats // game -> lower-case user -> stats
	fights  map[string]*matchFights            // match id -> its fights
	ended   map[string]time.Time               // match id -> when it ended (its fights are dropped a while later)
}

// statsLinger: how long after its end a match still takes fight reports (a launcher that was offline for a moment).
const statsLinger = 10 * time.Minute

const maxFightsPerMatch = 500

var errNotInMatch = errors.New("esa partida no es tuya")

func openStats(dir string) (*statsStore, error) {
	s := &statsStore{path: filepath.Join(dir, "stats.json"), players: map[string]map[string]*playerStats{},
		fights: map[string]*matchFights{}, ended: map[string]time.Time{}}
	if err := readJSON(s.path, &s.players); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *statsStore) playerLocked(game, user string) *playerStats {
	g := s.players[game]
	if g == nil {
		g = map[string]*playerStats{}
		s.players[game] = g
	}
	p := g[lc(user)]
	if p == nil {
		p = &playerStats{User: user}
		g[lc(user)] = p
	}
	return p
}

// matchStarted: a new match takes fight reports from its two players.
func (s *statsStore) matchStarted(id, game, p1, p2 string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fights[id] = &matchFights{game: game, p1: p1, p2: p2, reports: map[int][2]*fightReport{}, counted: map[int]bool{}}
}

// matchEnded: both players get the match and its time. Reports still arrive for statsLinger.
func (s *statsStore) matchEnded(id, game, p1, p2 string, started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secs := int64(time.Since(started).Seconds())
	if secs < 0 {
		secs = 0
	}
	now := time.Now().UTC()
	for _, u := range []string{p1, p2} {
		p := s.playerLocked(game, u)
		p.Matches++
		p.Seconds += secs
		p.Last = now
	}
	s.ended[id] = time.Now()
	for mid, t := range s.ended {
		if time.Since(t) > statsLinger {
			delete(s.ended, mid)
			delete(s.fights, mid)
		}
	}
	_ = writeJSON(s.path, s.players)
}

// report: one player's result for fight n of a match. It counts once the other player reported the same.
func (s *statsStore) report(user, id string, n int, r fightReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.fights[id]
	if m == nil {
		return errNoMatch
	}
	side := -1
	switch {
	case lc(user) == lc(m.p1):
		side = 0
	case lc(user) == lc(m.p2):
		side = 1
	}
	if side < 0 {
		return errNotInMatch
	}
	if n < 1 || n > maxFightsPerMatch || r.Winner < 0 || r.Winner > 2 || r.C1 < 0 || r.C1 > 0xffff || r.C2 < 0 || r.C2 > 0xffff {
		return errors.New("resultado no válido")
	}
	pair := m.reports[n]
	if pair[side] != nil {
		return nil // already in: a launcher resending after a dropped answer
	}
	pair[side] = &r
	m.reports[n] = pair
	if pair[0] == nil || pair[1] == nil || m.counted[n] || *pair[0] != *pair[1] || r.Winner == 0 {
		return nil
	}
	m.counted[n] = true
	p1, p2 := s.playerLocked(m.game, m.p1), s.playerLocked(m.game, m.p2)
	if r.Winner == 1 {
		p1.Wins++
		p2.Losses++
	} else {
		p2.Wins++
		p1.Losses++
	}
	for _, x := range []struct {
		p *playerStats
		c int
	}{{p1, r.C1}, {p2, r.C2}} {
		if x.c == 0 {
			continue
		}
		if x.p.Chars == nil {
			x.p.Chars = map[string]int{}
		}
		x.p.Chars[strconv.Itoa(x.c)]++
	}
	return writeJSON(s.path, s.players)
}

// statsView: what the launcher shows.
type statsView struct {
	User    string         `json:"user"`
	Wins    int            `json:"wins"`
	Losses  int            `json:"losses"`
	Matches int            `json:"matches"`
	Seconds int64          `json:"seconds"`
	Rank    int            `json:"rank"` // place in the table (0 = not in it: no fights yet)
	Chars   map[string]int `json:"chars,omitempty"`
}

// table: every player of a game with at least one fight, best first: more wins, then fewer losses, then more time.
func (s *statsStore) tableLocked(game string) []*playerStats {
	var out []*playerStats
	for _, p := range s.players[game] {
		if p.Wins+p.Losses > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Wins != b.Wins {
			return a.Wins > b.Wins
		}
		if a.Losses != b.Losses {
			return a.Losses < b.Losses
		}
		if a.Seconds != b.Seconds {
			return a.Seconds > b.Seconds
		}
		return lc(a.User) < lc(b.User)
	})
	return out
}

// view: the top of a game's table and one player's own line (with their place, wherever it is).
func (s *statsStore) view(game, user string, top int) (statsView, []statsView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tableLocked(game)
	me := statsView{User: user}
	if g := s.players[game]; g != nil {
		if p := g[lc(user)]; p != nil {
			me = statsView{User: p.User, Wins: p.Wins, Losses: p.Losses, Matches: p.Matches, Seconds: p.Seconds, Chars: p.Chars}
		}
	}
	var list []statsView
	for i, p := range t {
		if lc(p.User) == lc(user) {
			me.Rank = i + 1
		}
		if i < top {
			list = append(list, statsView{User: p.User, Wins: p.Wins, Losses: p.Losses, Matches: p.Matches, Seconds: p.Seconds, Rank: i + 1})
		}
	}
	return me, list
}
