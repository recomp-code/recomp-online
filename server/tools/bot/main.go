// bot: a scripted player for testing the launcher against a recomp-online server. It logs in (registering the
// account if needed), opens the live channel, and can wait for a match, invite someone, create a room or chat.
//
//	bot -user Bulma -pass capsule1 -waiting -invite Miguel -after 5s -room "Sala de Bulma" -say "hola"
//
// With -token it only prints a fresh session token for that account (to seed a launcher's login in tests).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var base string

func post(path, token string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func main() {
	flag.StringVar(&base, "server", "http://127.0.0.1:8090", "API base URL")
	user := flag.String("user", "Bulma", "account")
	pass := flag.String("pass", "capsule1", "password")
	game := flag.String("game", "dbzbt4", "game id")
	build := flag.String("build", "", "build string (the launcher's, to be compatible)")
	tokenOnly := flag.Bool("token", false, "print a session token and exit")
	waiting := flag.Bool("waiting", false, "mark as waiting for a match")
	invite := flag.String("invite", "", "invite this user")
	after := flag.Duration("after", 3*time.Second, "delay before inviting / creating the room")
	room := flag.String("room", "", "create a room with this name")
	roomPass := flag.String("roompass", "", "room password")
	roomMax := flag.Int("max", 2, "room size")
	join := flag.String("join", "", "join the room with this name")
	say := flag.String("say", "", "say this in the room")
	accept := flag.Bool("accept", false, "accept every invitation")
	flag.Parse()

	code, out := post("/api/login", "", map[string]string{"user": *user, "password": *pass})
	if code != 200 {
		code, out = post("/api/register", "", map[string]string{"user": *user, "password": *pass})
		if code != 200 {
			fmt.Fprintln(os.Stderr, "login/register:", code, out)
			os.Exit(1)
		}
	}
	tok := out["token"].(string)
	if *tokenOnly {
		fmt.Println(tok)
		return
	}
	u := "ws" + strings.TrimPrefix(base, "http") + "/api/ws?game=" + *game + "&build=" + *build
	c, _, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ws:", err)
		os.Exit(1)
	}
	if *waiting {
		post("/api/status", tok, map[string]bool{"waiting": true})
	}
	go func() {
		time.Sleep(*after)
		if *room != "" {
			fmt.Println("create room:", fmt.Sprint(post("/api/rooms", tok, map[string]any{"name": *room, "password": *roomPass, "max": *roomMax})))
			if *say != "" {
				time.Sleep(time.Second)
				post("/api/chat", tok, map[string]string{"text": *say})
			}
		}
		if *invite != "" {
			fmt.Println("invite:", fmt.Sprint(post("/api/invite", tok, map[string]any{"to": *invite, "settings": map[string]any{"fps60": true}})))
		}
	}()
	for {
		_, b, err := c.Read(context.Background())
		if err != nil {
			fmt.Println("closed:", err)
			return
		}
		var ev map[string]any
		_ = json.Unmarshal(b, &ev)
		fmt.Println("event:", ev["type"])
		switch ev["type"] {
		case "match": // for the game tests: relay, token and which player this side is
			m, _ := ev["match"].(map[string]any)
			fmt.Printf("match: %v %v %v\n", m["relay"], m["token"], m["player"])
		case "invite":
			if *accept {
				inv := ev["invite"].(map[string]any)
				post("/api/invite/respond", tok, map[string]any{"id": inv["id"], "accept": true})
			}
		case "lobby":
			if *join != "" {
				rooms, _ := ev["rooms"].([]any)
				for _, r := range rooms {
					rm := r.(map[string]any)
					if rm["name"] == *join {
						post("/api/rooms/join", tok, map[string]any{"id": rm["id"], "password": *roomPass})
						*join = ""
						if *say != "" {
							time.Sleep(500 * time.Millisecond)
							post("/api/chat", tok, map[string]string{"text": *say})
						}
					}
				}
			}
		}
	}
}
